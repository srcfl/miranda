// web/src/pair.js — pair a machine from the browser: decode the code, rendezvous
// in the /pair room, run the NNpsk0 initiator, and return the machine + safety
// number to compare with the agent's. Mirrors the Go `mir pair`.
import { runInitiator } from './pairing/nnpsk0.js';
import { decodeCode, roomID } from './pairing/code.js';
import { safetyNumber } from './pairing/sas.js';

const wsBase = (signalURL) => 'ws' + signalURL.slice(4); // http->ws, https->wss

// pairWithCode runs the pairing handshake using `ownerPub` as our identity.
// Returns { machine, safetyNumber }.
export async function pairWithCode(code, ownerPub) {
  const { signalURL, token } = decodeCode(code);

  const ws = new WebSocket(wsBase(signalURL) + '/pair?room=' + roomID(token));
  ws.binaryType = 'arraybuffer';
  await new Promise((res, rej) => {
    ws.onopen = res;
    ws.onerror = () => rej(new Error('could not reach the pairing relay'));
  });

  // MsgConn over the WebSocket: one binary message per send/recv (the /pair
  // bridge preserves message boundaries).
  const inbox = [];
  let waiter = null;
  ws.onmessage = (ev) => {
    const u = new Uint8Array(ev.data);
    if (waiter) { const w = waiter; waiter = null; w(u); } else inbox.push(u);
  };
  const mc = {
    send: (b) => ws.send(b),
    recv: () => new Promise((r) => (inbox.length ? r(inbox.shift()) : (waiter = r))),
  };

  try {
    const { info, binding } = await runInitiator(mc, token, ownerPub);
    return {
      machine: { machine_id: info.machine_id, host_pub: info.host_pub, name: info.name, signal: signalURL },
      safetyNumber: safetyNumber(binding),
    };
  } finally {
    try { ws.close(); } catch {}
  }
}
