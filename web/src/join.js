// web/src/join.js — claim a share invite from the browser (G1e). Mirrors the
// CLI's `mir join`: the guest rides the same blind pair room as pairing, proves
// its own key with pairing's msg1/msg3, presents its transport binding, shows
// the safety number (the OWNER holds the y/N on their side), and waits for the
// signed grant. Crypto is pairing's and grant.js's — nothing new.
import { startInitiator } from './pairing/nnpsk0.js';
import { decodeCode } from './pairing/code.js';
import { safetyNumber } from './pairing/sas.js';
import { openPairRoom } from './pair.js';
import { verifyGrant, validAt } from './identity/grant.js';

// The owner is a human deciding on a safety number: give them the same window
// the CLI invite has (5 min), not pairing's 30 s transport ceiling.
const JOIN_VERDICT_MS = 5 * 60 * 1000;

// joinWithCode runs the guest ceremony. `bindingRecord` is this browser's
// signed transport binding (the same record every attach presents), built by
// the caller. onSafety(sas) fires as soon as the number is comparable — before
// the verdict wait — so the guest can read it aloud. Returns
// { machine, grant } with machine carrying the owner for attach routing.
export async function joinWithCode(code, signer, bindingRecord, onSafety) {
  const { signalURL, token } = decodeCode(code);
  const room = await openPairRoom(signalURL, token, JOIN_VERDICT_MS);
  try {
    const started = await startInitiator(room.mc, token, signer);
    if (onSafety) onSafety(safetyNumber(started.binding));
    // The guest risks nothing by proceeding — the owner decides. Prove our key
    // (msg3), present the binding, then wait for the verdict.
    await started.finish(null);
    room.mc.send(new TextEncoder().encode(bindingRecord));

    let verdict;
    try {
      verdict = await room.mc.recv();
    } catch {
      throw new Error('the invite was declined or expired — nothing was set up');
    }
    let grant;
    try {
      grant = JSON.parse(new TextDecoder().decode(verdict));
    } catch {
      throw new Error('the share record did not verify — ask for a new invite');
    }
    if (!verifyGrant(grant)) {
      throw new Error('the share record did not verify — ask for a new invite');
    }
    if (grant.guest !== signer.address || grant.machine !== started.info.machine_id) {
      throw new Error('the share was minted for a different device or machine — ask for a new invite');
    }
    if (!validAt(grant, Math.floor(Date.now() / 1000))) {
      throw new Error('this share has already ended — ask for a new invite');
    }
    return {
      machine: {
        machine_id: started.info.machine_id,
        host_pub: started.info.host_pub,
        name: started.info.name,
        signal: signalURL,
        owner: grant.owner, // attach routes under the machine owner; we authenticate as the guest
      },
      grant,
    };
  } finally {
    room.close();
  }
}
