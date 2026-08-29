// ICESessionDead matches the Go peer.ICESessionDead helper: disconnected is
// recoverable (Wi-Fi/cellular flip); failed and closed tear the attach.
export function iceSessionDead(state) {
  return state === 'failed' || state === 'closed';
}

// iceSessionDegraded: the link dropped but may heal (or be raced back up) —
// the early-reaction window disconnect-grace.js acts on.
export function iceSessionDegraded(state) {
  return state === 'disconnected';
}
