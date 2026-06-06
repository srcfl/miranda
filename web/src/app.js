// go/web/src/app.js — browser attach: signaling + WebRTC P2P + Noise KK + xterm.
// Mirrors the Go client (internal/client Attach + ClientBridge). Milestone 2:
// a live shell in a browser tab. Dev owner key (localStorage); passkey is later.
import { HandshakeKK } from './noise/noise-kk.js';
import { encodeData, encodeResize, decodeFrame, FRAME_DATA } from './noise/frame.js';
import { x25519 } from '@noble/curves/ed25519';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { Terminal } from '@xterm/xterm';

const te = new TextEncoder();

// Dev owner identity, persisted in localStorage. (Passkey/prf comes in a later
// milestone; this is the CLI-style local key for now.)
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

// attach opens a P2P terminal to `machine` ({signal, machine_id, host_pub, name})
// and renders it into termEl. Returns a handle with test hooks.
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

  // non-trickle ICE: gather all candidates, then send the offer
  diag.step = 'creating-offer';
  await pc.setLocalDescription(await pc.createOffer());
  // non-trickle: send once gathering completes OR after a cap (use whatever
  // candidates we have — a slow/unreachable STUN must not hang the connect).
  await new Promise((res) => {
    if (pc.iceGatheringState === 'complete') return res();
    const done = () => { clearTimeout(t); res(); };
    const t = setTimeout(() => { diag.gather = 'timeout'; done(); }, 3000);
    pc.addEventListener('icegatheringstatechange', () => { diag.gather = pc.iceGatheringState; if (pc.iceGatheringState === 'complete') done(); });
  });
  diag.step = 'offer-sent';
  ws.send(JSON.stringify({ type: 'offer', sdp: pc.localDescription.sdp }));

  diag.step = 'awaiting-datachannel';
  await new Promise((res) => (dc.onopen = () => { diag.dc = 'open'; res(); }));
  diag.step = 'handshaking';

  // discrete-message recv over the DataChannel
  const inbox = [];
  let waiter = null;
  dc.onmessage = (ev) => {
    const u = new Uint8Array(ev.data);
    if (waiter) { const w = waiter; waiter = null; w(u); } else inbox.push(u);
  };
  const recv = () => new Promise((r) => (inbox.length ? r(inbox.shift()) : (waiter = r)));

  // Noise KK initiator: owner static key + the pinned agent host key
  const hs = new HandshakeKK({ initiator: true, s: owner, rs: hexToBytes(machine.host_pub) });
  dc.send(hs.writeMessage(new Uint8Array(0)));
  hs.readMessage(await recv());

  // bridge: xterm <-> Noise <-> DataChannel
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
  // test hooks (used by the Chrome validation)
  window.__term = term;
  window.__send = (s) => send(encodeData(te.encode(s)));
  window.__termText = () => {
    const b = term.buffer.active;
    let out = '';
    for (let i = 0; i < b.length; i++) out += b.getLine(i).translateToString(true) + '\n';
    return out;
  };
  window.__attached = true;
  return { term, pc, dc, ws };
}
