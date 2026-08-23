// web/src/pair.js — pair a machine from the browser: decode the code, rendezvous
// in the /pair room, run the NNpsk0 initiator, and return the machine + safety
// number to compare with the agent's. Mirrors the Go `mir pair`.
import { startInitiator } from './pairing/nnpsk0.js';
import { decodeCode, roomID } from './pairing/code.js';
import { safetyNumber } from './pairing/sas.js';
import { registryKey, sealRecord } from './identity/registry.js';

const wsBase = (signalURL) => 'ws' + signalURL.slice(4); // http->ws, https->wss

// PAIR_TIMEOUT_MS bounds the whole ceremony. The /pair bridge can stall — the agent
// never shows up, the relay drops mid-handshake — and without a cap the UI sits on
// "pairing…" forever. 30s is generous for a human-paced QR scan + two round trips.
const PAIR_TIMEOUT_MS = 30000;

// pairWithCode runs the pairing handshake using `signer` ({ address, priv }) as our
// identity: it sends the legacy-wire owner field and proves control with an auth
// signature over the channel binding. Returns { machine, safetyNumber }.
export async function pairWithCode(code, signer, secret = null) {
  const { signalURL, token } = decodeCode(code);

  const ws = new WebSocket(wsBase(signalURL) + '/pair?room=' + roomID(token));
  ws.binaryType = 'arraybuffer';

  // A single failure latch shared by the open, recv, timeout and post-open
  // close/error paths. `fail()` rejects whatever recv() is pending so runInitiator
  // unwinds instead of hanging; `failed` makes a later recv() reject synchronously.
  let failed = null;
  let waiter = null; // resolve/reject of the in-flight recv(), or null
  const fail = (err) => {
    if (!failed) failed = err;
    if (waiter) { const w = waiter; waiter = null; w.reject(failed); }
  };
  // 30s ceiling on the whole ceremony (mirrors connectOnce's connect timeout).
  const timer = setTimeout(() => fail(new Error('pairing timed out')), PAIR_TIMEOUT_MS);

  try {
    await new Promise((res, rej) => {
      ws.onopen = res;
      // Pre-open: a failure means the relay was unreachable.
      ws.onerror = () => rej(new Error('could not reach the pairing relay'));
      ws.onclose = () => rej(new Error('pairing relay closed the connection'));
    });

    // Post-open: re-wire close/error to the failure latch so a relay drop DURING the
    // handshake rejects the pending recv() (the pre-open handlers' rejection is moot
    // once open resolved). Without this the read side waited forever on a dead socket.
    ws.onerror = () => fail(new Error('pairing relay error'));
    ws.onclose = () => fail(new Error('pairing relay closed the connection'));

    // MsgConn over the WebSocket: one binary message per send/recv (the /pair
    // bridge preserves message boundaries).
    const inbox = [];
    ws.onmessage = (ev) => {
      const u = new Uint8Array(ev.data);
      if (waiter) { const w = waiter; waiter = null; w.resolve(u); } else inbox.push(u);
    };
    const mc = {
      send: (b) => ws.send(b),
      recv: () => new Promise((resolve, reject) => {
        if (inbox.length) return resolve(inbox.shift());
        if (failed) return reject(failed);
        waiter = { resolve, reject };
      }),
    };

    const provisioner = secret ? (info) => {
      const record = new TextEncoder().encode(JSON.stringify({
        v: 1,
        name: info.name,
        host_pub: info.host_pub,
        signal_url: signalURL,
        ts: Math.floor(Date.now() / 1000),
      }));
      const blob = sealRecord(registryKey(secret), crypto.getRandomValues(new Uint8Array(12)), record, info.machine_id);
      let binary = '';
      for (const b of blob) binary += String.fromCharCode(b);
      return btoa(binary);
    } : null;
    const started = await startInitiator(mc, token, signer);
    let finished = false;
    const abort = () => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      fail(new Error('pairing cancelled'));
      try { ws.close(); } catch {}
    };
    const commit = async () => {
      if (finished) throw new Error('pairing already finished');
      await started.finish(provisioner);
      finished = true;
      clearTimeout(timer);
      try { ws.close(); } catch {}
    };
    return {
      machine: { machine_id: started.info.machine_id, host_pub: started.info.host_pub, name: started.info.name, signal: signalURL },
      safetyNumber: safetyNumber(started.binding),
      commit,
      abort,
    };
  } catch (e) {
    clearTimeout(timer);
    try { ws.close(); } catch {}
    throw e;
  }
}
