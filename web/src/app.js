// web/src/app.js — the SPA: identity, a machine list, in-browser pairing, and a
// live terminal. Data plane is P2P + Noise (see attach); the relay only brokers.
import { HandshakeKK } from './noise/noise-kk.js';
import { encodeData, encodeResize, encodeControl, decodeFrame, FRAME_DATA, FRAME_WINDOWS, FRAME_HELLO } from './noise/frame.js';
import { awaitSocketOpen } from './net/ws-open.js';
import { runSession } from './net/reconnect.js';
import { backoff } from './net/backoff.js';
import { iceSessionDead } from './net/ice-state.js';
import { makeDisconnectGrace } from './net/disconnect-grace.js';
import { makePool, makeParkPolicy, stateView } from './net/pool.js';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { listMachines, addMachine } from './store.js';
import { fetchMachines, mergeMachines, freshDevices, sealMachineRecord } from './registry.js';
import { pairWithCode } from './pair.js';
import { confirmPairingSafety, machineAfterConfirmedPairing, pendingPairingConfirmation } from './pairing/confirm.js';
import { registerPasskey, signInPasskey, devOwnerKey, passkeySupported, isLocalhost } from './identity.js';
import { signBinding, recordJSON } from './identity/binding.js';
import { signAuth, attachChallenge } from './identity/auth.js';
import { filterRevoked, isMachineRevoked, loadRevocations, revokeMachine, syncRevocations } from './revocations.js';
import { makeKeybar, shouldShowKeybar } from './ui/keybar.js';
import jsQR from '/vendor/jsqr.js';

const te = new TextEncoder();
const td = new TextDecoder();
// Default STUN server for server-reflexive candidate discovery. Overridable via
// window.MIRANDA_STUN (set it to '' to disable) so a privacy-conscious deployment
// can point at its own STUN instead of a third party. A bare STUN request leaks
// the client IP to whoever runs the server, so this default is only ever used as
// a last resort — see iceServers.
const DEFAULT_STUN = (typeof window !== 'undefined' && typeof window.MIRANDA_STUN === 'string')
  ? window.MIRANDA_STUN
  : 'stun:stun.l.google.com:19302';

// The canonical one-line installer, mirrored from README.md's "Install" section
// (test/empty-state.test.js gates the two staying identical) so the empty state
// can offer it as a copy-paste block without duplicating install logic.
const INSTALL_CMD = 'curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | sh';

// iceServers builds the ICE config. It prefers the relay's own ephemeral TURN
// credentials: a TURN server already yields a server-reflexive candidate via its
// built-in STUN, so when TURN is available we do NOT add a standalone third-party
// STUN server — that would leak the client IP to a third party on every attach
// for no connectivity gain. Only when the relay offers no TURN do we fall back to
// the (configurable) default STUN. TURN, when used, relays ciphertext only —
// Noise keeps content end-to-end.
async function iceServers(signalURL) {
  const list = [];
  let haveTurn = false;
  try {
    const r = await fetch(signalURL.replace(/\/$/, '') + '/turn-credentials');
    if (r.ok) {
      const t = await r.json();
      if (t.urls && t.urls.length) {
        list.push({ urls: t.urls, username: t.username, credential: t.password });
        haveTurn = true;
      }
    }
  } catch {}
  if (!haveTurn && DEFAULT_STUN) list.unshift({ urls: DEFAULT_STUN });
  return list;
}

// --- identity -------------------------------------------------------------
// Resolved once at the sign-in gate and cached; ownerKey() stays synchronous so
// attach()/pairWithCode() are untouched (passkey get() is async + needs a user
// gesture, so it can't run inside a sync call).
// _id holds { owner, signer, secret }: an X25519 transport key, a neutral
// Ed25519 owner signer, and the in-memory-only passkey PRF result.
let _id = null;
export function ownerKey() {
  if (!_id) throw new Error('not signed in');
  return _id.owner;
}
export function signerKey() {
  if (!_id) throw new Error('not signed in');
  return _id.signer;
}
function setOwner(id) { _id = id; window.__ownerPub = bytesToHex(id.owner.pub); window.__identity = id.signer.address; }

// deviceID returns a stable per-browser device id (binding.device), generated once.
function deviceID() {
  let d = localStorage.getItem('tr_device_id');
  if (!d) { d = bytesToHex(crypto.getRandomValues(new Uint8Array(8))); localStorage.setItem('tr_device_id', d); }
  return d;
}

const wsBase = (signalURL) => 'ws' + signalURL.slice(4); // http->ws, https->wss

// --- attach (P2P + Noise + xterm) ----------------------------------------
// connectOnce establishes ONE P2P session into an existing `term`, routing
// keystrokes through the shared `current` ref (current.send). It RESOLVES when an
// established session ENDS (DataChannel/PeerConnection dropped) and REJECTS on a
// setup failure before the channel is live — a relay error (agent unavailable), the
// signal socket closing, or a 15s connect timeout — so a dead/absent agent fails the
// attempt FAST and runSession retries, instead of hanging at 'awaiting-datachannel'.
// onConnected() fires once the Noise channel is ready; onWindows(snapshot) gets each
// tmux FrameWindows snapshot. onLink('degraded'|'up'), optional, reports ICE link
// health on the LIVE session (a flip seen, a flip healed) so the pill stays honest
// before any teardown. onHello(meta), optional, gets late HELLOs — the agent
// re-announcing itself, today a machine rename (ours: the acknowledgement;
// another device's: a live update). The caller (makeTerminal) owns the terminal
// + teardown.
export async function connectOnce(machine, term, current, onConnected, onWindows, onLink, onHello) {
  const owner = ownerKey();
  const signer = signerKey();
	if (isMachineRevoked(machine.machine_id, signer.address)) throw new Error('machine revoked');
  // owner_id is the neutral Ed25519 address; the signed binding authorizes this
  // browser's X25519 transport key for the Noise handshake.
  const ownerId = signer.address;
  const binding = recordJSON(signBinding(signer, deviceID(), bytesToHex(owner.pub), Math.floor(Date.now() / 1000)));
  const diag = { step: 'start', ws: 'init', gather: '', iceConn: '', conn: '', dc: 'init' };
  window.__diag = diag;

  const ws = new WebSocket(
    wsBase(machine.signal) + '/attach?owner_id=' + encodeURIComponent(ownerId) +
    '&machine_id=' + encodeURIComponent(machine.machine_id),
  );
  // `aborted` lets the caller (handle.close on Back/switch) tear down a LIVE session:
  // session.stop() alone only flips a flag, but the read loop below blocks on recv()
  // until pc/dc actually close, so without this an aborted session leaks the
  // PeerConnection/DataChannel and keeps writing inbound frames to a disposed term.
  // current.abort closes ws/pc/dc, which fires endSession() -> unblocks recv() ->
  // connectOnce resolves and the finally runs. Guards term.write so nothing lands on
  // a disposed terminal after abort. Cleared in finally so a stale ref can't fire.
  let aborted = false;
  // See awaitSocketOpen: capture 'open' SYNCHRONOUSLY before the awaited iceServers()
  // fetch, or a fast (localhost) socket opens first and the one-shot event is missed.
  const wsOpen = awaitSocketOpen(ws).then(() => { diag.ws = 'open'; }, (e) => { diag.ws = 'error'; throw e; });
  const pc = new RTCPeerConnection({ iceServers: await iceServers(machine.signal) });
  const dc = pc.createDataChannel('terminal');
  dc.binaryType = 'arraybuffer';
  pc.oniceconnectionstatechange = () => { diag.iceConn = pc.iceConnectionState; };

  // `ended` resolves once when an ESTABLISHED session drops; it also unblocks a pending
  // recv() so the read loop unwinds and connectOnce resolves (-> prompt reconnect).
  let endSession;
  const ended = new Promise((res) => { endSession = res; });
  let connected = false;
  // Early reaction (R1): `disconnected` gets a short grace to heal, then the
  // session is ended so runSession redials promptly — instead of freezing until
  // the browser escalates to `failed`. Link events surface only once live: a
  // setup-phase wobble is the connect timeout's problem, not the pill's.
  const grace = makeDisconnectGrace({
    kill: () => endSession(),
    onDegraded: () => { if (connected) onLink && onLink('degraded'); },
    onRecovered: () => { if (connected) onLink && onLink('up'); },
  });
  pc.onconnectionstatechange = () => {
    diag.conn = pc.connectionState;
    grace.onState(pc.connectionState);
    if (iceSessionDead(pc.connectionState)) endSession();
  };
  dc.onclose = () => endSession();

  // Abort handle for the caller: close the transports (which triggers endSession via
  // the close handlers above) and mark aborted so the read loop stops writing.
  current.abort = () => {
    aborted = true;
    try { dc.close(); } catch {}
    try { pc.close(); } catch {}
    try { ws.close(); } catch {}
    endSession();
  };

  // `setupFail` rejects if the connect never completes (relay error / closed socket /
  // timeout). Racing it below makes a missing agent fail fast (reject -> backoff + retry)
  // rather than hang. Guarded so a late call (after the intentional ws.close on connect)
  // can't become an unhandled rejection.
  let failSetup;
  const setupFail = new Promise((_, rej) => { failSetup = rej; });
  setupFail.catch(() => {});
	let readySession;
	let resolveReady;
	const ready = new Promise((resolve) => { resolveReady = resolve; });
  ws.onmessage = async (ev) => {
    const m = JSON.parse(ev.data);
	if (m.type === 'ready' && m.session) { readySession = m.session; resolveReady(m.session); }
	else if (m.type === 'answer') { diag.step = 'got-answer'; await pc.setRemoteDescription({ type: 'answer', sdp: m.sdp }); }
    else if (m.type === 'error') { diag.step = 'signal-error'; failSetup(new Error('relay: ' + (m.reason || 'agent unavailable'))); }
  };
  ws.onclose = () => { if (!connected) failSetup(new Error('signal socket closed')); };
  const connectTimeout = setTimeout(() => failSetup(new Error('connect timeout')), 15000);

  try {
    diag.step = 'ws-connecting';
    await Promise.race([wsOpen, setupFail]);
	await Promise.race([ready, setupFail]);
    diag.step = 'creating-offer';
    await pc.setLocalDescription(await pc.createOffer());
    // non-trickle: send once gathering completes OR after a cap (a slow/unreachable STUN must not hang).
    await new Promise((res) => {
      if (pc.iceGatheringState === 'complete') return res();
      const finish = () => { clearTimeout(t); res(); };
      const t = setTimeout(() => { diag.gather = 'timeout'; finish(); }, 3000);
      pc.addEventListener('icegatheringstatechange', () => { diag.gather = pc.iceGatheringState; if (pc.iceGatheringState === 'complete') finish(); });
    });
    diag.step = 'offer-sent';
	const authSig = signAuth(signer, attachChallenge(readySession, machine.machine_id, pc.localDescription.sdp));
	let authBinary = '';
	for (const b of authSig) authBinary += String.fromCharCode(b);
	ws.send(JSON.stringify({ type: 'offer', sdp: pc.localDescription.sdp, binding, auth: btoa(authBinary) }));

    diag.step = 'awaiting-datachannel';
    const inbox = [];
    let waiter = null; // { resolve, reject } of the in-flight recv(), or null
    dc.onmessage = (ev) => { const u = new Uint8Array(ev.data); if (waiter) { const w = waiter; waiter = null; w.resolve(u); } else inbox.push(u); };
    // Subscribe to `ended` ONCE: re-subscribing per recv() leaked a closure per frame
    // on a high-throughput session. The single handler rejects whichever waiter is
    // pending at drop time; a recv() that arrives AFTER the drop rejects synchronously.
    let sessionEnded = false;
    ended.then(() => { sessionEnded = true; if (waiter) { const w = waiter; waiter = null; w.reject(new Error('session ended')); } });
    const recv = () => new Promise((resolve, reject) => {
      if (inbox.length) return resolve(inbox.shift());
      if (sessionEnded) return reject(new Error('session ended'));
      waiter = { resolve, reject };
    });
    await Promise.race([
      new Promise((res) => (dc.onopen = () => { diag.dc = 'open'; res(); })),
      ended.then(() => { throw new Error('closed before datachannel'); }),
      setupFail,
    ]);
    diag.step = 'handshaking';
    const hs = new HandshakeKK({ initiator: true, s: owner, rs: hexToBytes(machine.host_pub) });
    dc.send(hs.writeMessage(new Uint8Array(0)));
    hs.readMessage(await recv());

    // Channel live: stop the setup guards, publish send, drop signalling, tell the caller.
    clearTimeout(connectTimeout);
    current.send = (framed) => dc.send(hs.encrypt(framed));
    try { ws.close(); } catch {} // signalling done; the data plane is the DC
    diag.step = 'connected';
    current.send(encodeResize(term.cols, term.rows)); // size the (re)connected PTY to the live term
    connected = true;
    onConnected && onConnected();

    for (;;) {
      const ct = await recv();
      if (aborted) break; // caller tore us down: don't touch a possibly-disposed term
      const { type, payload } = decodeFrame(hs.decrypt(ct));
      if (type === FRAME_DATA) { if (!aborted) term.write(payload); }
      else if (type === FRAME_WINDOWS) { try { onWindows && onWindows(JSON.parse(td.decode(payload))); } catch {} }
      // A HELLO after the first one is the agent re-announcing itself — today
      // that means a machine rename (ours: the acknowledgement; another
      // device's: a live update).
      else if (type === FRAME_HELLO) { try { onHello && onHello(JSON.parse(td.decode(payload))); } catch {} }
    }
  } catch (e) {
    if (!connected) throw e; // setup failure -> runSession backs off and retries with a fresh offer
    // else: an established session dropped -> swallow so connectOnce RESOLVES (prompt reconnect)
  } finally {
    clearTimeout(connectTimeout);
    grace.dispose();
    current.send = null;
    current.abort = null; // this connection's transports are gone; drop the stale handle
    try { dc.close(); } catch {}
    try { pc.close(); } catch {}
    try { ws.close(); } catch {}
    window.__attached = false;
  }
}

// makeTerminal builds the DURABLE terminal: the xterm, its fit/resize wiring, and a
// `current` ref whose .send is swapped per (re)connect. Keystrokes are bound ONCE and
// route through current.send, so they survive reconnects without rebinding handlers.
export function makeTerminal(termEl) {
  const term = new Terminal({ fontSize: 13, cursorBlink: true, theme: { background: '#0b0e14' } });
  const fitAddon = new FitAddon();
  term.loadAddon(fitAddon);
  term.open(termEl);
  // Mobile input hygiene: a terminal must not autocapitalize/autocorrect/spellcheck
  // keystrokes, and iOS auto-zooms when focusing an input whose font-size is < 16px —
  // so pin the (hidden) helper textarea to 16px to suppress that zoom WITHOUT disabling
  // the user's own pinch-zoom. Best-effort; term.textarea exists after open().
  if (term.textarea) {
    term.textarea.setAttribute('autocapitalize', 'off');
    term.textarea.setAttribute('autocorrect', 'off');
    term.textarea.setAttribute('autocomplete', 'off');
    term.textarea.setAttribute('spellcheck', 'false');
    term.textarea.style.fontSize = '16px';
  }
  const refit = () => { try { fitAddon.fit(); } catch {} };
  refit();
  setTimeout(refit, 80); // catch layout/font settling
  const current = { send: null };
  term.onData((d) => current.send && current.send(encodeData(te.encode(d))));
  term.onResize(({ cols, rows }) => current.send && current.send(encodeResize(cols, rows)));
  // keep the terminal fitted to the viewport (desktop resize, iOS rotate/keyboard)
  let rT;
  const onViewport = () => { clearTimeout(rT); rT = setTimeout(() => { refit(); current.send && current.send(encodeResize(term.cols, term.rows)); }, 120); };
  window.addEventListener('resize', onViewport);
  window.visualViewport && window.visualViewport.addEventListener('resize', onViewport);
  window.addEventListener('orientationchange', onViewport);
  const dispose = () => {
    clearTimeout(rT);
    window.removeEventListener('resize', onViewport);
    window.visualViewport && window.visualViewport.removeEventListener('resize', onViewport);
    window.removeEventListener('orientationchange', onViewport);
    term.dispose();
  };
  // test hooks (unchanged surface)
  window.__term = term;
  window.__send = (s) => current.send && current.send(encodeData(te.encode(s)));
  window.__termText = () => {
    const b = term.buffer.active;
    let out = '';
    for (let i = 0; i < b.length; i++) out += b.getLine(i).translateToString(true) + '\n';
    return out;
  };
  return { term, current, refit, dispose };
}

// attach keeps the single-session contract for callers/tests (window.trAttach): a
// durable terminal + exactly one connectOnce, resolving when CONNECTED (not on drop)
// so the caller gets a handle. viewTerminal uses runSession for reconnect instead.
export async function attach(machine, termEl, onWindows) {
  const { term, current, dispose } = makeTerminal(termEl);
  term.write('[mir] connecting to ' + (machine.name || machine.machine_id) + '…\r\n');
  await new Promise((resolve, reject) => {
    connectOnce(machine, term, current, () => { window.__attached = true; term.focus(); resolve(); }, onWindows).catch(reject);
  });
  return {
    term,
    sendText: (s) => current.send && current.send(encodeData(te.encode(s))),
    sendCtl: (obj) => current.send && current.send(encodeControl(te.encode(JSON.stringify(obj)))),
    // abort() first so the live connectOnce tears down its transports and stops
    // writing before dispose() frees the terminal (mirror of viewTerminal's close).
    close: () => { try { current.abort && current.abort(); } catch {} current.send = null; dispose(); },
  };
}

// --- UI -------------------------------------------------------------------
const el = (tag, props = {}, ...kids) => {
  const n = Object.assign(document.createElement(tag), props);
  for (const k of kids) n.append(k);
  return n;
};

// --- warm machine sessions (R2) -------------------------------------------
// The pool lives at module scope so sessions survive navigation: leave the
// terminal for the machine list (or pairing) and come back — still live, still
// scrolled where you were. Each session owns its DURABLE terminal (the xterm
// and its DOM node persist; only current.send swaps per reconnect), its own
// runSession loop, and its own view state (windows snapshot, name, connection
// state). Frames route to THEIR session — never to whichever machine is on
// screen. The view (viewTerminal) is a shell over the ACTIVE session.
const POOL_MAX = 3;
const sessions = new Map(); // machine_id -> machine session
const poolPolicy = makePool({ max: POOL_MAX });

const activeSession = () => sessions.get(poolPolicy.activeId()) || null;
const isActive = (sess) => sess === activeSession();
const ping = (sess) => { sess.notify && sess.notify(); };

// startLoop wires one machine's reconnect loop. All the R1 behavior (early
// degraded reaction, honest transitions, outage timing, the terminal lines)
// lives here per session; what the PILL shows is derived from session state by
// stateView, so a background machine's trouble can show on its strip chip
// without ever touching the active machine's pill.
function startLoop(sess) {
  sess.parked = false;
  const outageSecs = () => { const s = (performance.now() - sess.dropAt) / 1000; sess.dropAt = 0; return s.toFixed(1); };
  sess.loop = runSession({
    connectOnce: (onConnected) => connectOnce(sess.machine, sess.term, sess.current, onConnected,
      (s) => { sess.snap = s; ping(sess); },
      (link) => {
        // Link honesty on the LIVE session (R1): `disconnected` surfaces the
        // moment it happens; a self-healed link goes straight back to live. No
        // terminal line for a blip that healed — the pill/chip carries it.
        if (link === 'degraded') { sess.degraded = true; if (!sess.dropAt) sess.dropAt = performance.now(); }
        else if (link === 'up') { sess.degraded = false; if (sess.dropAt) console.debug('[mir] link healed in ' + outageSecs() + 's'); }
        ping(sess);
      },
      // A late HELLO is a rename: ours (the ack) or another device's (live
      // update). The store is only written on OUR rename (we know its sealed
      // ts); here the session's name just follows the machine.
      (meta) => { if (meta && meta.name) { sess.machine = { ...sess.machine, name: meta.name }; ping(sess); } }),
    onState: (state, attempt) => {
      sess.attempt = attempt || 0;
      if (state === 'connected') {
        sess.conn = 'connected'; sess.degraded = false;
        if (isActive(sess)) { window.__attached = true; sess.term.focus(); }
        if (sess.reconnecting) {
          sess.reconnecting = false;
          if (sess.dropAt) { const s = outageSecs(); console.debug('[mir] resumed in ' + s + 's'); sess.term.write('\r\n[mir] reconnected (' + s + 's)\r\n'); }
          else sess.term.write('\r\n[mir] reconnected\r\n');
        }
      } else if (state === 'connecting') sess.conn = 'connecting';
      else if (state === 'reconnecting') {
        // Edge-trigger the terminal line on the connected -> reconnecting
        // transition ONLY; the loop re-emits 'reconnecting' per retry.
        const firstLossLine = !sess.reconnecting;
        sess.reconnecting = true;
        if (!sess.dropAt) sess.dropAt = performance.now();
        sess.conn = 'reconnecting';
        if (isActive(sess)) window.__attached = false;
        if (firstLossLine) sess.term.write('\r\n[mir] connection lost — reconnecting…\r\n');
      } else if (state === 'failed') {
        sess.dropAt = 0; sess.conn = 'failed';
        if (isActive(sess)) window.__attached = false;
        sess.term.write('\r\n[mir] couldn\'t reconnect — tap ⊘ to retry\r\n');
      }
      ping(sess);
    },
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    backoffFor: (attempt) => backoff(attempt),
  });
}

// openSession returns the warm session for a machine, creating (and starting)
// it if needed. Activation may evict the least-recently-used background
// machine — that one is CLOSED for real (loop, transports, terminal, DOM).
function openSession(machine) {
  const id = machine.machine_id;
  let sess = sessions.get(id);
  if (!sess) {
    const termEl = el('div', { className: 'termbox' });
    const t = makeTerminal(termEl);
    sess = {
      machine, termEl,
      term: t.term, current: t.current, refit: t.refit, disposeTerm: t.dispose,
      snap: null,
      conn: 'connecting', degraded: false, attempt: 0, parked: false,
      reconnecting: false, dropAt: 0,
      loop: null,
      notify: null, // set by the mounted terminal view (mountGen-guarded)
    };
    sess.term.write('[mir] connecting to ' + (machine.name || machine.machine_id) + '…\r\n');
    sessions.set(id, sess);
    startLoop(sess);
  }
  const { evict } = poolPolicy.activate(id);
  for (const eid of evict) closeSession(eid);
  if (sess.parked) startLoop(sess);
  return sess;
}

// closeSession fully tears one machine down: loop stopped, live transports
// aborted, terminal disposed, DOM node dropped. Order matters (see attach()).
function closeSession(id) {
  const sess = sessions.get(id);
  if (!sess) return;
  sessions.delete(id);
  poolPolicy.drop(id);
  sess.notify = null;
  try { sess.loop && sess.loop.stop(); } catch {}
  try { sess.current.abort && sess.current.abort(); } catch {}
  try { sess.current.send = null; } catch {}
  sess.disposeTerm();
  sess.termEl.remove();
}

// parkSession stops a background machine's loop and transports but keeps its
// terminal and state — it redials the moment it is activated again.
function parkSession(sess) {
  if (sess.parked || !sess.loop) return;
  try { sess.loop.stop(); } catch {}
  try { sess.current.abort && sess.current.abort(); } catch {}
  sess.loop = null;
  sess.parked = true;
  sess.conn = 'connecting'; sess.degraded = false; sess.reconnecting = false; sess.dropAt = 0;
  ping(sess);
}

// Battery honesty (R2): after HIDDEN_PARK_MS hidden, background sessions park
// (the ACTIVE one is the OS's business); returning to the tab resumes every
// parked pool member, so switching stays instant when the user is back.
const parkPolicy = makeParkPolicy({
  onPark: () => { for (const sess of sessions.values()) if (!isActive(sess)) parkSession(sess); },
  onResume: () => { for (const sess of sessions.values()) if (sess.parked) { startLoop(sess); ping(sess); } },
});
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => parkPolicy.visibility(document.hidden));
}

// mountGen bumps on every mount() — a lightweight "is this still the live view"
// token. pollForMachine (below) captures it before an async fetch and checks it
// after, so a navigation away (pairing, a terminal, sign-out) mid-fetch is
// detected and the stale poll drops its result instead of stomping whatever the
// user navigated to.
let mountGen = 0;
function mount(root, node) { mountGen++; root.replaceChildren(node); }

// newDevices returns the discovered machines not seen before (for a notice) and
// persists the seen set in localStorage, so the notice fires once per device.
function newDevices(discovered) {
  let seen = [];
  try { seen = JSON.parse(localStorage.getItem('tr_seen') || '[]'); } catch {}
  const fresh = freshDevices(seen, discovered);
  if (fresh.length) {
    localStorage.setItem('tr_seen', JSON.stringify([...new Set([...seen, ...fresh.map((m) => m.machine_id)])]));
  }
  return fresh;
}

let visibleMachines = [];

// discoveryPaused: the last registry fetch failed while saved machines were
// shown. The list renders a one-line notice instead of passing stale off as
// live; any successful fetch clears it.
let discoveryPaused = false;

// commandBlock renders a one-line command with a Copy button: monospace text
// that also selects on tap, plus a button that copies via navigator.clipboard
// when available. Mobile Safari (and any non-secure/older context) may lack or
// reject that API, so the fallback selects the text so the OS's own copy
// affordance (or a long-press) still works.
function commandBlock(text) {
  const code = el('code', { className: 'cmdblock-text' }, text);
  const btn = el('button', { className: 'cmdblock-copy', type: 'button' }, 'Copy');
  const selectText = () => {
    try {
      const range = document.createRange();
      range.selectNodeContents(code);
      const sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
    } catch {}
  };
  const flash = (label) => {
    const prev = btn.textContent;
    btn.textContent = label;
    btn.disabled = true;
    setTimeout(() => { btn.textContent = prev; btn.disabled = false; }, 1400);
  };
  btn.addEventListener('click', async () => {
    try {
      if (!navigator.clipboard || !navigator.clipboard.writeText) throw new Error('no Clipboard API');
      await navigator.clipboard.writeText(text);
      flash('Copied ✓');
    } catch {
      selectText();
      flash('Selected — copy it');
    }
  });
  code.addEventListener('click', selectText); // tap-to-select even without pressing Copy
  return el('div', { className: 'cmdblock' }, code, btn);
}

// emptyMachinesView is the friendly first-run state: no machines yet. The pulsing
// dot signals it resolves on its own (see pollForMachine) — no manual reload once
// the machine registers.
function emptyMachinesView(root) {
  return el('div', { className: 'view' },
    ...retiredNotice(), // retiring your last machine lands here — keep the way back visible
    el('h1', { className: 'live-heading' }, el('span', { className: 'live-dot' }), 'Waiting for your first machine…'),
    el('p', { className: 'muted' }, 'Install Miranda on the machine you want to keep reachable:'),
    commandBlock(INSTALL_CMD),
    el('p', { className: 'muted' }, 'Run `mir up` on your machine and follow it.'),
    el('button', { className: 'link', onclick: () => viewPair(root) }, 'Already have a pairing code? →'));
}

const EMPTY_POLL_MS = 4000;

// pollForMachine keeps the empty state resolving live: refetches the owner's
// registry every EMPTY_POLL_MS while this is still the mounted view (see
// mountGen above), re-rendering only once a machine appears. Stops silently on
// navigation, and for good once it has rendered a machine.
function pollForMachine(root) {
  const myGen = mountGen;
  setTimeout(async () => {
    if (mountGen !== myGen) return; // navigated away while waiting — stop
    try {
      const revocations = await syncRevocations(location.origin, signerKey().address);
      const discovered = await fetchMachines(location.origin, signerKey(), _id.secret);
      const visibleDiscovered = filterRevoked(discovered, revocations);
      const merged = filterRevoked(mergeMachines(listMachines(), visibleDiscovered), revocations);
      if (mountGen !== myGen) return; // navigated away mid-fetch — drop this result, don't stomp
      discoveryPaused = false;
      if (!merged.length) { pollForMachine(root); return; } // still empty — keep the view untouched (a re-mount would eat a Copy tap) and wait
      renderMachines(root, merged, newDevices(visibleDiscovered));
    } catch {
      if (mountGen === myGen) pollForMachine(root); // relay hiccup — retry, same view still up
    }
  }, EMPTY_POLL_MS);
}

// --- machine retirement (N2) ----------------------------------------------
// Retiring = the existing signed revocation, wrapped in words a person can act
// on: what changes, what does not, and the way back. Only the sheet's confirm
// button signs or records anything; a warm session (R2) closes first so
// nothing keeps typing into a machine the owner just retired. revokeMachine
// persists the signed record locally BEFORE any network — so a relay failure
// still leaves the machine retired on this device, and the notice says so.
let lastRetired = null; // {name, warn} — one gentle re-pair pointer on the next list render

// noticeSheet: one plain sentence in the app's own sheet idiom — never the
// browser's blocking dialog, which freezes the page and looks nothing like
// the app.
function noticeSheet(msg) {
  const sheet = el('div', { className: 'sheet', onclick: (e) => { if (e.target === sheet) sheet.remove(); } },
    el('div', { className: 'sheet-card' },
      el('p', { className: 'muted' }, msg),
      el('button', { className: 'btn', onclick: () => sheet.remove() }, 'OK')));
  document.body.append(sheet);
}

function retireSheet(host, machine, onDone) {
  const name = machine.name || machine.machine_id;
  const confirm = el('button', { className: 'btn danger', onclick: async () => {
    confirm.disabled = true;
    closeSession(machine.machine_id);
    let warn = null;
    try {
      await revokeMachine([machine.signal, location.origin], signerKey(), machine.machine_id);
    } catch (e) {
      warn = (e && e.message) || String(e); // retired locally; only publication failed
    }
    lastRetired = { name, warn };
    sheet.remove();
    onDone();
  } }, 'Retire this machine');
  const sheet = el('div', { className: 'sheet', onclick: (e) => { if (e.target === sheet) sheet.remove(); } },
    el('div', { className: 'sheet-card' },
      el('div', { className: 'sheet-title' }, 'retire ' + name + '?'),
      el('p', { className: 'muted' }, 'It disappears from your machine list on every device, and your identity can no longer reach it.'),
      el('p', { className: 'muted' }, 'The machine itself keeps running — tmux sessions and everything on it stay untouched.'),
      el('p', { className: 'muted' }, 'Want it back later? Run `mir up` on it and pair fresh.'),
      confirm,
      el('button', { className: 'link', onclick: () => sheet.remove() }, 'cancel')));
  host.append(sheet);
}

// retiredNotice renders (and clears) the one-time pointer after a retirement,
// so the next thing the user reads is how to come back if they expected the
// machine to still be there.
function retiredNotice() {
  if (!lastRetired) return [];
  const r = lastRetired;
  lastRetired = null;
  const out = [el('p', { className: 'muted' }, '⊘ Retired ' + r.name + '. To use it again: run `mir up` on it and pair fresh.')];
  if (r.warn) out.push(el('p', { className: 'muted' }, 'Heads-up: ' + r.warn));
  return out;
}

function renderMachines(root, machines, fresh) {
	visibleMachines = machines;
  if (!machines.length) { mount(root, emptyMachinesView(root)); return; }
  const viewEl = el('div', { className: 'view' });
  const grid = el('div', { className: 'grid' });
  for (const m of machines) {
    // A machine that is warm in the session pool (R2) shows its live state on
    // the card — tapping it switches back in place, scrollback intact.
    const warm = sessions.get(m.machine_id);
    const card = el('button', { className: 'card machine', onclick: () => viewTerminal(root, m) },
      el('div', { className: 'name' }, (warm && warm.machine.name) || m.name || m.machine_id),
      el('div', { className: 'sub' }, m.machine_id.slice(0, 12) + '…'));
    if (warm) card.append(el('div', { className: 'sub' }, el('span', { className: 'dot ' + stateView(warm).chip }), ' live'));
    card.append(el('span', { className: 'link retire', onclick: (e) => {
      e.stopPropagation();
      retireSheet(viewEl, m, () => viewMachines(root));
    } }, 'retire'));
    grid.append(card);
  }
  grid.append(el('button', { className: 'card add', onclick: () => viewPair(root) },
    el('div', { className: 'plus' }, '＋'), el('div', { className: 'sub' }, 'Pair a machine')));
  const kids = [
    el('h1', {}, 'your machines'),
    el('p', { className: 'muted' }, 'Your live terminals. Leave one device, continue on another.'),
    ...retiredNotice(),
  ];
  if (fresh && fresh.length) {
    kids.push(el('p', { className: 'muted' }, '📣 new device joined: ' + fresh.map((m) => m.name || m.machine_id).join(', ')));
  }
  if (discoveryPaused) {
    kids.push(el('p', { className: 'muted' }, '⚠ The relay is unreachable — showing saved machines; discovery resumes when you are back online.'));
  }
  kids.push(grid);
  viewEl.append(...kids);
  mount(root, viewEl);
}

// viewMachines renders the locally-stored machines immediately, then enriches the
// list from the owner's encrypted registry (B2) — your machines appear by name with
// no manual pairing. The fetch is same-origin (the relay that served this app) and
// best-effort: a failure just leaves the local list. Discovery only. When the
// resulting list is empty, pollForMachine keeps refreshing it live (U3).
function viewMachines(root) {
	let localRevocations;
	try { localRevocations = loadRevocations(signerKey().address); }
	catch (e) {
		mount(root, el('div', { className: 'view' },
			el('h1', {}, 'security check failed'),
			el('p', { className: 'muted' }, e && e.message || String(e)),
			el('p', { className: 'muted' }, 'Your saved machine list did not pass its signature check, so it is not shown. Reload to retry; if this keeps happening, sign out and back in.')));
		return;
	}
	const local = filterRevoked(listMachines(), localRevocations);
	renderMachines(root, local, []);
  (async () => {
    try {
		const revocations = await syncRevocations(location.origin, signerKey().address);
      const discovered = await fetchMachines(location.origin, signerKey(), _id.secret);
		const visibleDiscovered = filterRevoked(discovered, revocations);
		const merged = filterRevoked(mergeMachines(listMachines(), visibleDiscovered), revocations);
		discoveryPaused = false;
		renderMachines(root, merged, newDevices(visibleDiscovered));
		if (!merged.length) pollForMachine(root);
    } catch {
		// Relay unreachable: keep the local list, but say so — a silently stale
		// list would read as live. pollForMachine keeps retrying either way.
		if (local.length && !discoveryPaused) { discoveryPaused = true; renderMachines(root, local, []); }
		if (!local.length) pollForMachine(root);
	}
  })();
}

// codeFromScan extracts the pairing code from a scanned QR, which encodes
// Web + "/#" + code (take the part after '#'); falls back to the raw text.
function codeFromScan(text) {
  const i = (text || '').indexOf('#');
  return (i >= 0 ? text.slice(i + 1) : text).trim();
}

// scanQR opens the rear camera and decodes QR frames, calling onCode on the
// first hit. Returns a stop() function. (iOS Safari has no BarcodeDetector, so
// we decode frames with jsQR on a canvas.)
async function scanQR(videoEl, onCode, onError) {
  let stream = null, raf = 0, stopped = false;
  const stop = () => { stopped = true; cancelAnimationFrame(raf); if (stream) stream.getTracks().forEach((t) => t.stop()); };
  try {
    stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } });
  } catch (e) { onError('The camera is unavailable — allow camera access in your browser settings, or type the code instead. (' + (e && e.message || e) + ')'); return stop; }
  videoEl.setAttribute('playsinline', '');
  videoEl.muted = true;
  videoEl.srcObject = stream;
  await videoEl.play().catch(() => {});
  const canvas = document.createElement('canvas');
  const ctx = canvas.getContext('2d', { willReadFrequently: true });
  const tick = () => {
    if (stopped) return;
    if (videoEl.readyState >= 2 && videoEl.videoWidth) {
      canvas.width = videoEl.videoWidth; canvas.height = videoEl.videoHeight;
      ctx.drawImage(videoEl, 0, 0);
      const img = ctx.getImageData(0, 0, canvas.width, canvas.height);
      const res = jsQR(img.data, img.width, img.height);
      if (res && res.data) { stop(); onCode(res.data); return; }
    }
    raf = requestAnimationFrame(tick);
  };
  tick();
  return stop;
}

function viewPair(root, prefill = '', auto = false) {
  const status = el('div', { className: 'status' });

  // Camera lifecycle: scanQR holds an open MediaStream (the rear camera + the iOS
  // recording indicator). It is only self-stopped on explicit cancel / successful
  // decode — so navigating away (Back, any re-render, switching to the paste flow,
  // or the tab going hidden) would otherwise leave the camera ON. Track the active
  // stop here and release it on EVERY transition out of the scanner. `scanGen` bumps
  // on each stop so a getUserMedia() that resolves AFTER a navigation is dropped
  // (stopped + not re-registered) instead of leaking a now-orphaned stream.
  let activeScanStop = null;
  let scanGen = 0;
  const stopScan = () => { scanGen++; const s = activeScanStop; activeScanStop = null; if (s) { try { s(); } catch {} } };
  const onVisibility = () => { if (document.hidden) stopScan(); };
  document.addEventListener('visibilitychange', onVisibility);
  // leaveScanner: stop the camera + detach the visibility listener, then navigate.
  // Used by every path that leaves the pairing flow for good (machines / pairing UI).
  const leaveScanner = (go) => { stopScan(); document.removeEventListener('visibilitychange', onVisibility); go(); };

  const pairCode = async (raw) => {
    const code = (raw || '').trim();
    if (!code) return;
    stopScan(); // a code arrived (scan or paste) -> the camera's job is done
    mount(root, el('div', { className: 'view' }, el('h1', {}, 'pairing…'), status));
    status.textContent = 'pairing…';
    try {
      const ceremony = await pairWithCode(code, signerKey(), _id.secret);
      const { machine, safetyNumber } = ceremony;
      const pending = pendingPairingConfirmation(machine, safetyNumber);
      window.__lastSafety = safetyNumber;
      status.innerHTML = '';
      status.append(
        el('div', { className: 'ok' }, 'Compare safety numbers before trusting ' + (machine.name || machine.machine_id)),
        el('div', { className: 'sas' }, pending.safetyNumber),
        el('div', { className: 'muted' }, 'Find the safety number printed by `mir pair` on the machine. Continue only if all six groups match exactly.'),
        el('div', { className: 'actions' },
          el('button', { className: 'btn', onclick: async () => {
            if (ceremony.commit) await ceremony.commit();
            const confirmed = confirmPairingSafety(pending);
            const persisted = machineAfterConfirmedPairing(confirmed);
            addMachine(persisted);
            status.innerHTML = '';
            status.append(
              el('div', { className: 'ok' }, '✓ paired ' + (persisted.name || persisted.machine_id)),
              el('button', { className: 'btn', onclick: () => leaveScanner(() => viewMachines(root)) }, 'Done'));
          } }, 'Safety number matches'),
          el('button', { className: 'link', onclick: () => {
            if (ceremony.abort) ceremony.abort();
            leaveScanner(() => viewPair(root));
          } }, 'Cancel pairing')));
    } catch (e) {
      status.innerHTML = '';
      const msg = (e && e.message) || String(e);
      status.append(
        el('div', { className: 'muted' }, 'Pairing failed: ' + msg + '. Codes expire after 5 min — make sure it’s fresh and the machine is still showing it.'),
        el('button', { className: 'btn', onclick: () => leaveScanner(() => viewPair(root)) }, 'Try again'));
    }
  };

  const startScan = async () => {
    stopScan(); // never run two cameras: release any prior scanner first
    const myGen = scanGen; // this scanner's generation; a later stop() invalidates it
    const video = el('video', { className: 'scanner' });
    const sStatus = el('div', { className: 'status' });
    mount(root, el('div', { className: 'view' },
      el('h1', {}, 'scan the QR'),
      el('p', { className: 'muted' }, 'Point at the QR shown by `mir pair` on the machine.'),
      video, sStatus,
      el('button', { className: 'link', onclick: () => leaveScanner(() => viewPair(root)) }, '✕ cancel')));
    const stop = await scanQR(video, (text) => pairCode(codeFromScan(text)), (err) => { sStatus.textContent = err; });
    // If we were navigated away while getUserMedia was resolving, scanGen moved on:
    // stop this orphaned stream right now. Otherwise register it as the active scanner.
    if (myGen !== scanGen) { try { stop(); } catch {} }
    else activeScanStop = stop;
  };

  if (auto && prefill) { pairCode(prefill); return; } // arrived via QR URL -> pair now

  const input = el('input', { className: 'code', placeholder: 'or paste the code', value: prefill });
  mount(root, el('div', { className: 'view' },
    el('h1', {}, 'pair a machine'),
    el('p', { className: 'muted' }, 'Run `mir pair` on the machine, then scan its QR.'),
    el('button', { className: 'btn', onclick: startScan }, '📷 Scan QR'),
    input,
    el('button', { className: 'link', onclick: () => pairCode(input.value) }, 'pair with pasted code'),
    status,
    el('button', { className: 'link back', onclick: () => leaveScanner(() => viewMachines(root)) }, '← machines')));
}

function viewTerminal(root, machineToOpen) {
  // The view is a SHELL over the warm session pool: machineToOpen becomes the
  // active session (joining the pool — possibly evicting the LRU background
  // machine); every pooled machine keeps its terminal alive in the DOM, hidden
  // except the active one. Back leaves them all warm.
  openSession(machineToOpen);
  const m = () => activeSession().machine;
  const focus = () => { const s = activeSession(); s && s.term.focus(); };
  // Retire (N2): the same confirmation sheet as the machine list — plain words,
  // and the signed revocation only on the confirm button. The sheet closes this
  // machine's warm session itself; Back to the list shows the way-back notice.
  const retire = () => retireSheet(view, m(), () => viewMachines(root));

  // tmux control: the AGENT runs the command directly (robust — no prefix
  // dependence, no command-prompt/Enter fragility, no keystroke timing). Target
  // windows by stable window_id (@N), not index, to dodge renumber races; carry
  // the owning session so the agent can switch our client across sessions.
  // Routed to the ACTIVE machine's channel — read live, per press.
  const ctl = (o) => { const s = activeSession(); if (!s) return; s.current.send && s.current.send(encodeControl(te.encode(JSON.stringify(o)))); s.term.focus(); };
  const selectWin = (sess, id) => ctl({ a: 'select-window', s: sess, t: id });
  const newWin = (sess) => ctl({ a: 'new-window', s: sess });
  const renameWin = (id, n) => ctl({ a: 'rename-window', t: id, n });
  const killWin = (id) => ctl({ a: 'kill-window', t: id });
  const switchSess = (name) => ctl({ a: 'switch-session', t: name });
  const newSess = () => ctl({ a: 'new-session' });
  const renameSess = (cur, n) => ctl({ a: 'rename-session', t: cur, n });
  const killSess = (name) => ctl({ a: 'kill-session', t: name });
  const safeName = (s) => (s || '').replace(/[^\w .\-]/g, '').slice(0, 32);

  // sessionsView normalizes the ACTIVE machine's snapshot to a session list. v2
  // is native; a v1 snapshot (flat {win,active}) from an un-upgraded agent maps
  // to one session so the UI keeps working through a staged rollout.
  const sessionsView = () => {
    const snap = activeSession() && activeSession().snap;
    if (!snap) return null;
    if (snap.sess) return snap.sess;
    if (snap.win) return [{ n: '', act: true, aw: snap.active, win: snap.win }];
    return null;
  };
  const hasAlert = (s, kind) => (s.win || []).some((w) => w[kind]); // kind: 'b' bell, 'a' activity

  // Back keeps every pooled session warm — that is the whole point of R2.
  const termHost = el('div', { className: 'termhost' });
  const back = el('button', { className: 'tb-btn', onclick: () => viewMachines(root) }, '‹ Machines');
  const sw = el('button', { className: 'tb-btn', title: 'switch machine', onclick: () => openSwitcher() }, '⇄');
  const revokeBtn = el('button', { className: 'tb-btn', title: 'retire machine', onclick: retire }, '⊘');
  const titleEl = el('div', { className: 'tb-title' }, m().name || m().machine_id);
  const renameBtn = el('button', { className: 'tb-btn', title: 'rename machine', onclick: () => renameMachineUI() }, '✎');
  // machbar: one chip per warm machine (name + state dot), shown only when two
  // or more are pooled — a single machine keeps today's clean layout.
  const machbar = el('div', { className: 'machbar', hidden: true });
  const strip = el('div', { className: 'winbar' });
  const view = el('div', { className: 'view term' },
    el('div', { className: 'topbar' }, back, titleEl, renameBtn, sw, revokeBtn),
    machbar, strip, termHost);
  mount(root, view);
  const viewGen = mountGen; // this mount's token: stale session notifies no-op

  // Rename everywhere: local first (this device shows the new name at once and
  // keeps winning the merge on name_ts), then deliver the owner-resealed
  // registry record over the session — the agent can't seal records itself. It
  // persists the name, republishes on its live relay registration, and
  // re-HELLOs (the onHello below) — your other devices converge on the newer ts.
  function renameMachineUI() {
    const sess = activeSession();
    const n = (prompt('Rename machine', sess.machine.name || '') || '').trim();
    if (!n || n === sess.machine.name) return;
    if ([...n].length > 64 || /[\u0000-\u001F\u007F]/.test(n)) {
      noticeSheet('Names are 1–64 characters with no control characters. Try a shorter, plainer name.');
      return;
    }
    let sealed;
    try {
      sealed = sealMachineRecord(_id.secret, {
        machine_id: sess.machine.machine_id, name: n, host_pub: sess.machine.host_pub, signal_url: sess.machine.signal,
      });
    } catch (e) {
      noticeSheet('The rename was not saved — reload and try again. (' + (e && e.message || e) + ')');
      return;
    }
    sess.machine = { ...sess.machine, name: n, name_ts: sealed.ts };
    addMachine(sess.machine); // upsert by machine_id — the local rename is instant
    renderChrome();
    ctl({ a: 'rename-machine', n, blob: sealed.blob });
  }

  // tab strip: a pill per window of the ACTIVE session (mirrors the snapshot),
  // active highlighted; a session chip (when >1 session) switches sessions and
  // surfaces a dot when a BACKGROUND session has activity/bell; ＋ new window, ▦
  // grid overview. Falls back to ＋/‹/› before any snapshot (or non-tmux shells).
  function renderStrip() {
    strip.replaceChildren();
    const sess = sessionsView();
    const cur = sess && (sess.find((s) => s.act) || sess[0]);
    if (!cur || !cur.win || !cur.win.length) {
      strip.append(
        el('span', { className: 'winbar-label' }, 'windows'),
        el('button', { className: 'tb-btn', onclick: () => newWin() }, '＋'),
        el('button', { className: 'tb-btn', onclick: () => ctl({ a: 'previous-window' }) }, '‹'),
        el('button', { className: 'tb-btn', onclick: () => ctl({ a: 'next-window' }) }, '›'));
      return;
    }
    const pills = el('div', { className: 'pills' });
    if (sess.length > 1) {
      const others = sess.filter((s) => !s.act);
      const bg = others.some((s) => hasAlert(s, 'b')) ? ' bell' : others.some((s) => hasAlert(s, 'a')) ? ' act' : '';
      const chip = el('button', { className: 'pill sess', title: 'switch session', onclick: openSessions },
        el('span', {}, '⧉ ' + (cur.n || 'session')));
      if (bg) chip.append(el('span', { className: 'dot' + bg }));
      pills.append(chip);
    }
    for (const w of cur.win) {
      const active = w.id === cur.aw;
      const pill = el('button', { className: 'pill' + (active ? ' active' : ''), onclick: () => selectWin(cur.n, w.id) },
        el('span', {}, w.i + ':' + (w.n || w.cmd || '')));
      if (w.b) pill.append(el('span', { className: 'dot bell' }));
      else if (w.a && !active) pill.append(el('span', { className: 'dot act' }));
      if (active) setTimeout(() => pill.scrollIntoView({ inline: 'center', block: 'nearest' }), 0);
      pills.append(pill);
    }
    pills.append(el('button', { className: 'pill add', title: 'new window', onclick: () => newWin(cur.n) }, '＋'));
    strip.append(pills, el('button', { className: 'tb-btn', title: 'overview', onclick: openGrid }, '▦'));
  }
  renderStrip();

  // grid overview: one section per session (header with rename / kill / new
  // window), a card per window under it (name, running command, panes). Tap a
  // window to switch — across sessions if needed. ＋ New session at the bottom.
  function openGrid() {
    const sess = sessionsView();
    if (!sess) return;
    const card = el('div', { className: 'sheet-card' }, el('div', { className: 'sheet-title' }, 'sessions on ' + (m().name || m().machine_id)));
    const sheet = el('div', { className: 'sheet', onclick: (e) => { if (e.target === sheet) sheet.remove(); } }, card);
    for (const s of sess) {
      const head = el('div', { className: 'sheet-subtitle' }, el('span', {}, (s.act ? '● ' : '') + (s.n || 'session')));
      const acts = el('span', { className: 'sub-acts' },
        el('span', { className: 'link', onclick: (e) => { e.stopPropagation(); const n = safeName(prompt('Rename session', s.n)); if (n) renameSess(s.n, n); sheet.remove(); } }, 'rename'));
      // killing the viewed session detaches our client (ends the attach) — offer it only for background sessions
      if (!s.act) acts.append(el('span', { className: 'link', onclick: (e) => { e.stopPropagation(); if (confirm('Kill session "' + s.n + '" and all its windows?')) killSess(s.n); sheet.remove(); } }, 'kill'));
      head.append(acts);
      const grid = el('div', { className: 'wgrid' });
      for (const w of (s.win || [])) {
        const wc = el('button', { className: 'wcard' + (w.id === s.aw ? ' active' : ''), onclick: () => { sheet.remove(); selectWin(s.n, w.id); } },
          el('div', { className: 'wcard-name' }, w.i + ': ' + (w.n || '')),
          el('div', { className: 'wcard-sub' }, (w.cmd || '') + (w.p > 1 ? ' · ' + w.p + ' panes' : '') + (w.b ? ' · 🔔' : w.a ? ' · •' : '')),
          el('div', { className: 'wcard-actions' },
            el('span', { className: 'link', onclick: (e) => { e.stopPropagation(); const n = safeName(prompt('Rename window', w.n)); if (n) renameWin(w.id, n); sheet.remove(); } }, 'rename'),
            el('span', { className: 'link', onclick: (e) => { e.stopPropagation(); if (confirm('Close window ' + w.i + '?')) killWin(w.id); sheet.remove(); } }, 'close')));
        grid.append(wc);
      }
      card.append(head, grid, el('button', { className: 'sheet-item add', onclick: () => { sheet.remove(); newWin(s.n); } }, '＋ New window'));
    }
    card.append(
      el('button', { className: 'sheet-item add', onclick: () => { sheet.remove(); newSess(); } }, '＋ New session'),
      el('button', { className: 'link', onclick: () => sheet.remove() }, 'cancel'));
    view.append(sheet);
  }

  // session switcher: jump our client straight to another tmux session (one tap
  // from the strip's session chip), or spin up a new one.
  function openSessions() {
    const sess = sessionsView();
    if (!sess) return;
    const card = el('div', { className: 'sheet-card' }, el('div', { className: 'sheet-title' }, 'switch session'));
    const sheet = el('div', { className: 'sheet', onclick: (e) => { if (e.target === sheet) sheet.remove(); } }, card);
    for (const s of sess) {
      const alert = hasAlert(s, 'b') ? ' 🔔' : hasAlert(s, 'a') ? ' •' : '';
      card.append(el('button', { className: 'sheet-item' + (s.act ? ' active' : ''), onclick: () => { sheet.remove(); if (!s.act) switchSess(s.n); else focus(); } },
        (s.act ? '● ' : '') + (s.n || 'session') + ' · ' + (s.win || []).length + 'w' + alert));
    }
    card.append(
      el('button', { className: 'sheet-item add', onclick: () => { sheet.remove(); newSess(); } }, '＋ New session'),
      el('button', { className: 'link', onclick: () => sheet.remove() }, 'cancel'));
    view.append(sheet);
  }

  // quick-switcher: jump straight to another machine without going back to the
  // list. Warm machines switch in place (<200 ms, no teardown); a warm
  // BACKGROUND machine also gets a "disconnect" to leave the pool deliberately.
  function openSwitcher() {
    const card = el('div', { className: 'sheet-card' }, el('div', { className: 'sheet-title' }, 'switch machine'));
    const sheet = el('div', { className: 'sheet', onclick: (e) => { if (e.target === sheet) sheet.remove(); } }, card);
    for (const mm of visibleMachines.filter((x) => x.machine_id !== m().machine_id)) {
      const warm = sessions.get(mm.machine_id);
      const item = el('button', { className: 'sheet-item', onclick: () => { sheet.remove(); switchTo(mm); } },
        el('span', {}, (warm ? '● ' : '') + (warm && warm.machine.name || mm.name || mm.machine_id)));
      if (warm) {
        item.append(el('span', { className: 'link', onclick: (e) => {
          e.stopPropagation(); sheet.remove(); closeSession(mm.machine_id); renderChrome();
        } }, 'disconnect'));
      }
      card.append(item);
    }
    card.append(el('button', { className: 'sheet-item add', onclick: () => { sheet.remove(); viewPair(root); } }, '＋ Pair a machine'));
    card.append(el('button', { className: 'link', onclick: () => sheet.remove() }, 'cancel'));
    view.append(sheet);
  }

  // mobile keyboard accessory bar: Esc / Ctrl / Tab / arrows / extras, only on
  // touch (coarse-pointer) devices so desktop keeps a clean terminal. Its presses
  // go through the SAME reconnect-safe path as typed keys — the ACTIVE session's
  // current.send is read LIVE each press and the raw bytes are framed with
  // encodeData, byte-identical to term.onData — so the bar keeps working across
  // reconnects AND machine switches (nothing is captured per machine).
  if (shouldShowKeybar()) {
    const { el: keybar } = makeKeybar(
      (bytes) => { const s = activeSession(); s && s.current.send && s.current.send(encodeData(bytes)); },
      () => focus(),
    );
    view.append(keybar);
  }

  // topbar status pill (R1): the ACTIVE machine's state via stateView — the
  // strings are R1's, pinned by pool.test.js. Tap to retry when it has given up.
  const pill = el('button', { className: 'pill status', onclick: () => { const s = activeSession(); if (pill.dataset.failed && s && s.loop) s.loop.retryNow(); } }, '…');
  view.querySelector('.topbar').insertBefore(pill, sw);

  const renderPill = () => {
    const s = activeSession();
    if (!s) return;
    const v = stateView(s);
    pill.className = 'pill status ' + v.cls;
    pill.textContent = v.pill;
    pill.dataset.failed = s.conn === 'failed' ? '1' : '';
  };

  // machbar: a chip per pooled machine in pool order — state dot + name; tap to
  // switch. Hidden entirely below two machines (todays clean single layout).
  const renderMachbar = () => {
    machbar.hidden = sessions.size < 2;
    if (machbar.hidden) { machbar.replaceChildren(); return; }
    const pills = el('div', { className: 'pills' });
    for (const id of poolPolicy.ids()) {
      const sess = sessions.get(id);
      if (!sess) continue;
      const activeChip = isActive(sess);
      const v = stateView(sess);
      pills.append(el('button', { className: 'pill mach' + (activeChip ? ' active' : ''), onclick: () => { if (!activeChip) switchTo(sess.machine); } },
        el('span', { className: 'dot ' + v.chip }), el('span', {}, sess.machine.name || sess.machine.machine_id.slice(0, 8))));
    }
    machbar.replaceChildren(el('span', { className: 'winbar-label' }, 'machines'), pills);
  };

  const renderTitle = () => { const mm = m(); titleEl.textContent = mm.name || mm.machine_id; };
  function renderChrome() { renderTitle(); renderPill(); renderMachbar(); renderStrip(); }

  // Every pooled terminal lives in the DOM, hidden except the active one — the
  // durable-terminal design across machines: scrollback survives switching.
  const adoptTerminals = () => {
    for (const id of poolPolicy.ids()) {
      const sess = sessions.get(id);
      if (!sess) continue;
      if (sess.termEl.parentNode !== termHost) termHost.append(sess.termEl);
      sess.termEl.hidden = !isActive(sess);
    }
  };

  // wireNotify points every pooled session's notify at THIS mounted view; a
  // stale notify (view replaced) no-ops via the mountGen token. Frames always
  // land in their OWN session's state — only the rendering is view-scoped.
  const wireNotify = () => {
    for (const sess of sessions.values()) {
      sess.notify = () => {
        if (mountGen !== viewGen) return;
        renderMachbar();
        if (isActive(sess)) { renderPill(); renderTitle(); renderStrip(); }
      };
    }
  };

  // Keep the window.__term/__send/__termText validation hooks on the ACTIVE
  // terminal (makeTerminal points them at creation; switching re-points).
  const pointTestHooks = (sess) => {
    window.__term = sess.term;
    window.__send = (s) => sess.current.send && sess.current.send(encodeData(te.encode(s)));
    window.__termText = () => {
      const b = sess.term.buffer.active;
      let out = '';
      for (let i = 0; i < b.length; i++) out += b.getLine(i).translateToString(true) + '\n';
      return out;
    };
  };

  // switchTo: activate a machine IN PLACE — no teardown, no redial for a warm
  // one (a parked one starts dialing immediately). Pure local DOM work, well
  // under the 200 ms switch budget.
  function switchTo(mm) {
    openSession(mm);
    adoptTerminals();
    wireNotify();
    renderChrome();
    const s = activeSession();
    window.__attached = s.conn === 'connected';
    pointTestHooks(s);
    requestAnimationFrame(() => { s.refit(); s.current.send && s.current.send(encodeResize(s.term.cols, s.term.rows)); s.term.focus(); });
  }

  switchTo(machineToOpen);
}

// after sign-in: replay a scanned pairing code, else show machines
function afterSignIn(root, pendingFrag) {
  if (pendingFrag) viewPair(root, pendingFrag, true);
  else viewMachines(root);
}

function viewIdentityGate(root, pendingFrag) {
  const status = el('div', { className: 'status' });
  const done = (k, mode) => { setOwner(k); localStorage.setItem('tr_identity_mode', mode); afterSignIn(root, pendingFrag); };
  const useDev = () => done(devOwnerKey(), 'dev');
  const busy = (on) => root.querySelectorAll('button').forEach((b) => { b.disabled = on; });

  // Log IN first: a discoverable get() surfaces your iCloud-synced passkey, so
  // EVERY device derives the SAME owner_id. Only offer Create if login finds none
  // (creating per-device would mint a different passkey -> a different identity).
  const create = async () => {
    busy(true); status.textContent = 'creating your passkey…';
    try { done(await registerPasskey(), 'passkey'); }
    catch (e) { busy(false); status.textContent = 'could not create a passkey: ' + (e && e.message || e); }
  };
  const login = async () => {
    busy(true); status.textContent = 'Face ID / Touch ID…';
    try { done(await signInPasskey(), 'passkey'); }
    catch (e) {
      busy(false);
      status.innerHTML = '';
      status.append(
        el('div', { className: 'muted' }, 'No passkey found on this account (' + (e && e.message || e) + ').'),
        el('button', { className: 'btn', onclick: create }, 'Create a passkey'));
    }
  };

  const kids = [
    el('h1', {}, 'Miranda'),
    el('p', { className: 'muted' }, 'Leave your desk. Keep your terminal. Your passkey finds your machines; the relay never receives the private identity key or plaintext terminal data.'),
  ];
  if (passkeySupported && !isLocalhost()) {
    kids.push(el('button', { className: 'btn', onclick: login }, 'Log in with passkey'));
    kids.push(el('p', { className: 'muted' }, 'New here? Logging in offers to create one.'));
  }
  // The local dev key is a plaintext, non-biometric x25519 key in localStorage.
  // Offer it ONLY on localhost — never on a public origin, even if the browser
  // lacks WebAuthn — so a real owner identity is never persisted in the clear on
  // a production host. devOwnerKey() is additionally hard-guarded to localhost.
  if (isLocalhost()) {
    kids.push(el('button', { className: passkeySupported ? 'link' : 'btn', onclick: useDev },
      'Continue with a local dev key (localhost)'));
  } else if (!passkeySupported) {
    kids.push(el('p', { className: 'muted' },
      'This browser does not support passkeys (WebAuthn PRF). Open Miranda in a passkey-capable browser (e.g. Safari 18+ or Chrome) to log in.'));
  }
  kids.push(status);
  mount(root, el('div', { className: 'view' }, ...kids));
}

export function start(root) {
  // a code can arrive via the URL fragment (#<code>) — e.g. scanning the QR.
  // Stash it and strip the fragment; replay it after sign-in (pairing needs the key).
  const frag = decodeURIComponent((location.hash || '').replace(/^#/, ''));
  if (frag) history.replaceState(null, '', location.pathname + location.search);

  viewIdentityGate(root, frag); // do NOT auto-run the ceremony — needs a user gesture
  window.__ready = true;

  // test/validation hooks (used after sign-in)
  // __useDevKey mints/persists a plaintext owner key, so expose it ONLY on
  // localhost — never let console access mint a real identity on a public origin.
  if (isLocalhost()) {
    window.__useDevKey = () => { setOwner(devOwnerKey()); localStorage.setItem('tr_identity_mode', 'dev'); viewMachines(root); };
  }
  window.trAttach = (m) => attach(m, root.querySelector('.termbox') || root);
  window.trPair = (code) => pairWithCode(code, signerKey(), _id.secret);
}
