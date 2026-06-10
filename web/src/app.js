// web/src/app.js — the SPA: identity, a machine list, in-browser pairing, and a
// live terminal. Data plane is P2P + Noise (see attach); the relay only brokers.
import { HandshakeKK } from './noise/noise-kk.js';
import { encodeData, encodeResize, encodeControl, decodeFrame, FRAME_DATA, FRAME_WINDOWS } from './noise/frame.js';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { listMachines, addMachine } from './store.js';
import { pairWithCode } from './pair.js';
import { confirmPairingSafety, machineAfterConfirmedPairing, pendingPairingConfirmation } from './pairing/confirm.js';
import { registerPasskey, signInPasskey, devOwnerKey, passkeySupported, isLocalhost } from './identity.js';
import jsQR from '/vendor/jsqr.js';

const te = new TextEncoder();
const td = new TextDecoder();
const DEFAULT_STUN = 'stun:stun.l.google.com:19302';

// iceServers builds the ICE config: a default STUN plus ephemeral TURN creds
// fetched from the signaling server (for symmetric-NAT / cellular reachability).
// TURN only ever relays ciphertext — Noise keeps content end-to-end.
async function iceServers(signalURL) {
  const list = [{ urls: DEFAULT_STUN }];
  try {
    const r = await fetch(signalURL.replace(/\/$/, '') + '/turn-credentials');
    if (r.ok) {
      const t = await r.json();
      if (t.urls && t.urls.length) list.push({ urls: t.urls, username: t.username, credential: t.password });
    }
  } catch {}
  return list;
}

// --- identity -------------------------------------------------------------
// Resolved once at the sign-in gate and cached; ownerKey() stays synchronous so
// attach()/pairWithCode() are untouched (passkey get() is async + needs a user
// gesture, so it can't run inside a sync call).
let _owner = null;
export function ownerKey() {
  if (!_owner) throw new Error('not signed in');
  return _owner;
}
function setOwner(k) { _owner = k; window.__ownerPub = bytesToHex(k.pub); }

const wsBase = (signalURL) => 'ws' + signalURL.slice(4); // http->ws, https->wss

// --- attach (P2P + Noise + xterm) ----------------------------------------
// onWindows(snapshot) is invoked with each FrameWindows tmux snapshot.
export async function attach(machine, termEl, onWindows) {
  const owner = ownerKey();
  const ownerHex = bytesToHex(owner.pub);

  const term = new Terminal({ fontSize: 13, cursorBlink: true, theme: { background: '#0b0e14' } });
  const fitAddon = new FitAddon();
  term.loadAddon(fitAddon);
  term.open(termEl);
  const refit = () => { try { fitAddon.fit(); } catch {} };
  refit();
  setTimeout(refit, 80); // catch layout/font settling
  term.write('[mir] connecting to ' + (machine.name || machine.machine_id) + '…\r\n');

  const diag = { step: 'start', ws: 'init', gather: '', iceConn: '', conn: '', dc: 'init' };
  window.__diag = diag;

  const ws = new WebSocket(
    wsBase(machine.signal) + '/attach?owner_id=' + encodeURIComponent(ownerHex) +
    '&machine_id=' + encodeURIComponent(machine.machine_id),
  );
  const pc = new RTCPeerConnection({ iceServers: await iceServers(machine.signal) });
  const dc = pc.createDataChannel('terminal');
  dc.binaryType = 'arraybuffer';
  ws.onerror = () => { diag.ws = 'error'; };
  pc.oniceconnectionstatechange = () => { diag.iceConn = pc.iceConnectionState; };
  pc.onconnectionstatechange = () => { diag.conn = pc.connectionState; };

  ws.onmessage = async (ev) => {
    const m = JSON.parse(ev.data);
    if (m.type === 'answer') { diag.step = 'got-answer'; await pc.setRemoteDescription({ type: 'answer', sdp: m.sdp }); }
    else if (m.type === 'error') { diag.step = 'signal-error'; term.write('\r\n[mir] signal error: ' + (m.reason || '') + '\r\n'); }
  };
  diag.step = 'ws-connecting';
  await new Promise((r) => (ws.onopen = () => { diag.ws = 'open'; r(); }));

  diag.step = 'creating-offer';
  await pc.setLocalDescription(await pc.createOffer());
  // non-trickle: send once gathering completes OR after a cap (a slow/unreachable
  // STUN must not hang the connect).
  await new Promise((res) => {
    if (pc.iceGatheringState === 'complete') return res();
    const finish = () => { clearTimeout(t); res(); };
    const t = setTimeout(() => { diag.gather = 'timeout'; finish(); }, 3000);
    pc.addEventListener('icegatheringstatechange', () => { diag.gather = pc.iceGatheringState; if (pc.iceGatheringState === 'complete') finish(); });
  });
  diag.step = 'offer-sent';
  ws.send(JSON.stringify({ type: 'offer', sdp: pc.localDescription.sdp }));

  diag.step = 'awaiting-datachannel';
  await new Promise((res) => (dc.onopen = () => { diag.dc = 'open'; res(); }));
  diag.step = 'handshaking';

  const inbox = [];
  let waiter = null;
  dc.onmessage = (ev) => {
    const u = new Uint8Array(ev.data);
    if (waiter) { const w = waiter; waiter = null; w(u); } else inbox.push(u);
  };
  const recv = () => new Promise((r) => (inbox.length ? r(inbox.shift()) : (waiter = r)));

  const hs = new HandshakeKK({ initiator: true, s: owner, rs: hexToBytes(machine.host_pub) });
  dc.send(hs.writeMessage(new Uint8Array(0)));
  hs.readMessage(await recv());

  const send = (framed) => dc.send(hs.encrypt(framed));
  term.onData((d) => send(encodeData(te.encode(d))));
  term.onResize(({ cols, rows }) => send(encodeResize(cols, rows)));
  refit(); // fit now that the bridge is live -> emits the initial RESIZE
  send(encodeResize(term.cols, term.rows));

  // keep the terminal fitted to the viewport (desktop resize, iOS rotate/keyboard)
  let rT;
  const onViewport = () => { clearTimeout(rT); rT = setTimeout(refit, 120); };
  window.addEventListener('resize', onViewport);
  window.visualViewport && window.visualViewport.addEventListener('resize', onViewport);
  window.addEventListener('orientationchange', onViewport);
  const stopResize = () => {
    clearTimeout(rT);
    window.removeEventListener('resize', onViewport);
    window.visualViewport && window.visualViewport.removeEventListener('resize', onViewport);
    window.removeEventListener('orientationchange', onViewport);
  };
  (async () => {
    for (;;) {
      let ct;
      try { ct = await recv(); } catch { return; }
      const { type, payload } = decodeFrame(hs.decrypt(ct));
      if (type === FRAME_DATA) term.write(payload);
      else if (type === FRAME_WINDOWS) { try { onWindows && onWindows(JSON.parse(td.decode(payload))); } catch {} }
    }
  })();

  term.focus();
  // test hooks
  window.__term = term;
  window.__send = (s) => send(encodeData(te.encode(s)));
  window.__termText = () => {
    const b = term.buffer.active;
    let out = '';
    for (let i = 0; i < b.length; i++) out += b.getLine(i).translateToString(true) + '\n';
    return out;
  };
  window.__attached = true;
  return {
    term, pc, dc, ws,
    sendText: (s) => send(encodeData(te.encode(s))), // feed keystrokes
    sendCtl: (obj) => send(encodeControl(te.encode(JSON.stringify(obj)))), // tmux window command

    close: () => { stopResize(); try { ws.close(); } catch {} try { pc.close(); } catch {} term.dispose(); },
  };
}

// --- UI -------------------------------------------------------------------
const el = (tag, props = {}, ...kids) => {
  const n = Object.assign(document.createElement(tag), props);
  for (const k of kids) n.append(k);
  return n;
};

function mount(root, node) { root.replaceChildren(node); }

function viewMachines(root) {
  const grid = el('div', { className: 'grid' });
  for (const m of listMachines()) {
    grid.append(el('button', { className: 'card machine', onclick: () => viewTerminal(root, m) },
      el('div', { className: 'name' }, m.name || m.machine_id),
      el('div', { className: 'sub' }, m.machine_id.slice(0, 12) + '…')));
  }
  grid.append(el('button', { className: 'card add', onclick: () => viewPair(root) },
    el('div', { className: 'plus' }, '＋'), el('div', { className: 'sub' }, 'Pair a machine')));
  mount(root, el('div', { className: 'view' },
    el('h1', {}, 'your machines'),
    el('p', { className: 'muted' }, 'Reach a shell on any of them — peer-to-peer, end-to-end encrypted.'),
    grid));
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
  } catch (e) { onError('camera unavailable: ' + (e && e.message || e)); return stop; }
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
  const pairCode = async (raw) => {
    const code = (raw || '').trim();
    if (!code) return;
    mount(root, el('div', { className: 'view' }, el('h1', {}, 'pairing…'), status));
    status.textContent = 'pairing…';
    try {
      const { machine, safetyNumber } = await pairWithCode(code, ownerKey().pub);
      const pending = pendingPairingConfirmation(machine, safetyNumber);
      window.__lastSafety = safetyNumber;
      status.innerHTML = '';
      status.append(
        el('div', { className: 'ok' }, 'Compare safety numbers before trusting ' + (machine.name || machine.machine_id)),
        el('div', { className: 'sas' }, pending.safetyNumber),
        el('div', { className: 'muted' }, 'Find the safety number printed by `mir-agent pair` on the machine. Continue only if every group matches this number exactly.'),
        el('div', { className: 'actions' },
          el('button', { className: 'btn', onclick: () => {
            const confirmed = confirmPairingSafety(pending);
            const persisted = machineAfterConfirmedPairing(confirmed);
            addMachine(persisted);
            status.innerHTML = '';
            status.append(
              el('div', { className: 'ok' }, '✓ paired ' + (persisted.name || persisted.machine_id)),
              el('button', { className: 'btn', onclick: () => viewMachines(root) }, 'Done'));
          } }, 'Safety number matches'),
          el('button', { className: 'link', onclick: () => viewPair(root) }, 'Cancel pairing')));
    } catch (e) {
      status.innerHTML = '';
      status.append(el('div', { className: 'muted' }, 'pairing failed: ' + (e && e.message || e)),
        el('button', { className: 'btn', onclick: () => viewPair(root) }, 'Try again'));
    }
  };

  const startScan = async () => {
    const video = el('video', { className: 'scanner' });
    const sStatus = el('div', { className: 'status' });
    let stop = null;
    mount(root, el('div', { className: 'view' },
      el('h1', {}, 'scan the QR'),
      el('p', { className: 'muted' }, 'Point at the QR shown by `mir-agent pair`.'),
      video, sStatus,
      el('button', { className: 'link', onclick: () => { if (stop) stop(); viewPair(root); } }, '✕ cancel')));
    stop = await scanQR(video, (text) => pairCode(codeFromScan(text)), (err) => { sStatus.textContent = err; });
  };

  if (auto && prefill) { pairCode(prefill); return; } // arrived via QR URL -> pair now

  const input = el('input', { className: 'code', placeholder: 'or paste the code', value: prefill });
  mount(root, el('div', { className: 'view' },
    el('h1', {}, 'pair a machine'),
    el('p', { className: 'muted' }, 'Run `mir-agent pair` on the machine, then scan its QR.'),
    el('button', { className: 'btn', onclick: startScan }, '📷 Scan QR'),
    input,
    el('button', { className: 'link', onclick: () => pairCode(input.value) }, 'pair with pasted code'),
    status,
    el('button', { className: 'link back', onclick: () => viewMachines(root) }, '← machines')));
}

function viewTerminal(root, machine) {
  let handle = null;
  let snap = null; // latest FrameWindows snapshot: v2 {v,sess:[...]}, or null (non-tmux)
  const close = () => { try { handle && handle.close(); } catch {} };
  const focus = () => { window.__term && window.__term.focus(); };

  // tmux control: the AGENT runs the command directly (robust — no prefix
  // dependence, no command-prompt/Enter fragility, no keystroke timing). Target
  // windows by stable window_id (@N), not index, to dodge renumber races; carry
  // the owning session so the agent can switch our client across sessions.
  const ctl = (o) => { handle && handle.sendCtl(o); focus(); };
  const selectWin = (sess, id) => ctl({ a: 'select-window', s: sess, t: id });
  const newWin = (sess) => ctl({ a: 'new-window', s: sess });
  const renameWin = (id, n) => ctl({ a: 'rename-window', t: id, n });
  const killWin = (id) => ctl({ a: 'kill-window', t: id });
  const switchSess = (name) => ctl({ a: 'switch-session', t: name });
  const newSess = () => ctl({ a: 'new-session' });
  const renameSess = (cur, n) => ctl({ a: 'rename-session', t: cur, n });
  const killSess = (name) => ctl({ a: 'kill-session', t: name });
  const safeName = (s) => (s || '').replace(/[^\w .\-]/g, '').slice(0, 32);

  // sessionsView normalizes the snapshot to a session list. v2 is native; a v1
  // snapshot (flat {win,active}) from an un-upgraded agent maps to one session so
  // the UI keeps working through a staged rollout.
  const sessionsView = () => {
    if (!snap) return null;
    if (snap.sess) return snap.sess;
    if (snap.win) return [{ n: '', act: true, aw: snap.active, win: snap.win }];
    return null;
  };
  const hasAlert = (s, kind) => (s.win || []).some((w) => w[kind]); // kind: 'b' bell, 'a' activity

  const termBox = el('div', { className: 'termbox' });
  const back = el('button', { className: 'tb-btn', onclick: () => { close(); viewMachines(root); } }, '‹ Machines');
  const sw = el('button', { className: 'tb-btn', title: 'switch machine', onclick: () => openSwitcher() }, '⇄');
  const strip = el('div', { className: 'winbar' });
  const view = el('div', { className: 'view term' },
    el('div', { className: 'topbar' }, back, el('div', { className: 'tb-title' }, machine.name || machine.machine_id), sw),
    strip, termBox);
  mount(root, view);

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
    const card = el('div', { className: 'sheet-card' }, el('div', { className: 'sheet-title' }, 'sessions on ' + (machine.name || machine.machine_id)));
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

  // quick-switcher: jump straight to another machine without going back to the list
  function openSwitcher() {
    const card = el('div', { className: 'sheet-card' }, el('div', { className: 'sheet-title' }, 'switch machine'));
    const sheet = el('div', { className: 'sheet', onclick: (e) => { if (e.target === sheet) sheet.remove(); } }, card);
    for (const m of listMachines().filter((x) => x.machine_id !== machine.machine_id)) {
      card.append(el('button', { className: 'sheet-item', onclick: () => { sheet.remove(); close(); viewTerminal(root, m); } }, m.name || m.machine_id));
    }
    card.append(el('button', { className: 'sheet-item add', onclick: () => { sheet.remove(); close(); viewPair(root); } }, '＋ Pair a machine'));
    card.append(el('button', { className: 'link', onclick: () => sheet.remove() }, 'cancel'));
    view.append(sheet);
  }

  attach(machine, termBox, (s) => { snap = s; renderStrip(); })
    .then((h) => { handle = h; })
    .catch((e) => termBox.append(el('div', { className: 'status' }, 'connect failed: ' + (e && e.message || e))));
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
    el('p', { className: 'muted' }, 'Your terminals, on every device — peer-to-peer, end-to-end encrypted. Log in on any device and your machines appear. The relay never sees your identity.'),
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
  window.trPair = (code) => pairWithCode(code, ownerKey().pub);
}
