// web/src/pairing/confirm.js — tiny state helper for the browser pairing gate.

export function pendingPairingConfirmation(machine, safetyNumber) {
  if (!machine || typeof machine !== 'object') throw new Error('missing machine');
  if (typeof safetyNumber !== 'string' || safetyNumber.length === 0) {
    throw new Error('missing safety number');
  }
  return Object.freeze({ machine, safetyNumber, confirmed: false });
}

export function confirmPairingSafety(pending) {
  if (!pending || pending.confirmed !== false) throw new Error('no pending pairing confirmation');
  return Object.freeze({ machine: pending.machine, safetyNumber: pending.safetyNumber, confirmed: true });
}

export function machineAfterConfirmedPairing(state) {
  if (!state || state.confirmed !== true) throw new Error('pairing safety number not confirmed');
  return state.machine;
}
