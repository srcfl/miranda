// web/src/app.js — the SPA: identity, a machine list, in-browser pairing, and a
// live terminal. Data plane is P2P + Noise (see attach); the relay only brokers.
import { HandshakeKK } from './noise/noise-kk.js';
import { encodeData, encodeResize, decodeFrame, FRAME_DATA } from './noise/frame.js';
import { x25519 } from '@noble/curves/ed25519';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { Terminal } from '@xterm/xterm';
import { listMachines, addMachine } from './store.js';
import { pairWithCode } from './pair.js';

const te = new TextEncoder();

// --- identity -------------------------------------------------------------
// Dev owner key in localStorage. Milestone 4 swaps this for a passkey (prf)
// while keeping the same {priv, pub} shape.
export function ownerKey() {
  let h = localStorage.getItem('tr_owner');
  if (!h) {
    h = bytesToHex(x25519.utils.randomPrivateKey());
    localStorage.setItem('tr_owner', h);
  }
  const priv = hexToBytes(h);
  return { priv, pub: x25519.getPublicKey(priv) };
}

const wsBase = (signalURL) => 'ws' + signalURL.slice(4); // http->ws, https->wss

// --- attach (P2P + Noise + xterm) ----------------------------------------
export async function attach(machine, termEl) {
  const owner = ownerKey();
  const ownerHex = bytesToHex(owner.pub);

  const term = new Terminal({ fontSize: 13, cursorBlink: true, theme: { background: '#0b0e14' } });
  term.open(termEl);
  term.write('[trm] connecting to ' + (machine.name || machine.machine_id) + '…\r\n');

  const diag = { step: 'start', ws: 'init', gather: '', iceConn: '', conn: '', dc: 'init' };
  window.__diag = diag;

  const ws = new WebSocket(
    wsBase(machine.signal) + '/attach?owner_id=' + encodeURIComponent(ownerHex) +
    '&machine_id=' + encodeURIComponent(machine.machine_id),
  );
  const pc = new RTCPeerConnection(machine.stun ? { iceServers: [{ urls: machine.stun }] } : {});
  const dc = pc.createDataChannel('terminal');
  dc.binaryType = 'arraybuffer';
  ws.onerror = () => { diag.ws = 'error'; };
  pc.oniceconnectionstatechange = () => { diag.iceConn = pc.iceConnectionState; };
  pc.onconnectionstatechange = () => { diag.conn = pc.connectionState; };

  ws.onmessage = async (ev) => {
    const m = JSON.parse(ev.data);
    if (m.type === 'answer') { diag.step = 'got-answer'; await pc.setRemoteDescription({ type: 'answer', sdp: m.sdp }); }
    else if (m.type === 'error') { diag.step = 'signal-error'; term.write('\r\n[trm] signal error: ' + (m.reason || '') + '\r\n'); }
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
  send(encodeResize(term.cols, term.rows));
  term.onData((d) => send(encodeData(te.encode(d))));
  term.onResize(({ cols, rows }) => send(encodeResize(cols, rows)));
  (async () => {
    for (;;) {
      let ct;
      try { ct = await recv(); } catch { return; }
      const { type, payload } = decodeFrame(hs.decrypt(ct));
      if (type === FRAME_DATA) term.write(payload);
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
  return { term, pc, dc, ws, close: () => { try { ws.close(); } catch {} try { pc.close(); } catch {} term.dispose(); } };
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

function viewPair(root, prefill = '', auto = false) {
  const input = el('input', { className: 'code', placeholder: 'paste the code from `tr-agent pair`', value: prefill });
  const status = el('div', { className: 'status' });
  const doPair = async () => {
    const code = input.value.trim();
    if (!code) return;
    go.disabled = true;
    status.textContent = 'pairing…';
    try {
      const { machine, safetyNumber } = await pairWithCode(code, ownerKey().pub);
      addMachine(machine);
      window.__lastSafety = safetyNumber;
      status.innerHTML = '';
      status.append(
        el('div', { className: 'ok' }, '✓ paired ' + (machine.name || machine.machine_id)),
        el('div', { className: 'sas' }, safetyNumber),
        el('div', { className: 'muted' }, 'Check this safety number matches the one on the machine.'),
        el('button', { className: 'btn', onclick: () => viewMachines(root) }, 'Done'));
    } catch (e) {
      go.disabled = false;
      status.textContent = 'pairing failed: ' + (e && e.message || e);
    }
  };
  const go = el('button', { className: 'btn', onclick: doPair }, 'Pair');
  mount(root, el('div', { className: 'view' },
    el('h1', {}, auto ? 'pairing…' : 'pair a machine'),
    el('p', { className: 'muted' }, auto ? 'Scanned from a machine — pairing it now.' : 'Run `tr-agent pair` on the machine, then scan its QR or paste the code.'),
    input, go, status,
    el('button', { className: 'link', onclick: () => viewMachines(root) }, '← machines')));
  if (auto && prefill) doPair(); // arrived via QR -> pair straight away
}

function viewTerminal(root, machine) {
  const back = el('button', { className: 'link back', onclick: () => { try { handle && handle.close(); } catch {}; viewMachines(root); } }, '← machines');
  const termBox = el('div', { className: 'termbox' });
  mount(root, el('div', { className: 'view term' }, back, termBox));
  let handle;
  attach(machine, termBox).then((h) => { handle = h; }).catch((e) => termBox.append(el('div', { className: 'status' }, 'connect failed: ' + (e && e.message || e))));
}

export function start(root) {
  window.__ownerPub = bytesToHex(ownerKey().pub);
  // a code can arrive via the URL fragment (#<code>) — e.g. scanning the QR.
  // Pair straight away, then strip the fragment so a reload doesn't re-pair.
  const frag = decodeURIComponent((location.hash || '').replace(/^#/, ''));
  if (frag) {
    history.replaceState(null, '', location.pathname + location.search);
    viewPair(root, frag, true);
  } else {
    viewMachines(root);
  }
  window.__ready = true;
  // expose for validation
  window.trAttach = (m) => attach(m, root.querySelector('.termbox') || root);
  window.trPair = (code) => pairWithCode(code, ownerKey().pub);
}
