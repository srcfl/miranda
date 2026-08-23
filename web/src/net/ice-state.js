// ICESessionDead matches the Go peer.ICESessionDead helper: disconnected is
// recoverable (Wi-Fi/cellular flip); failed and closed tear the attach.
export function iceSessionDead(state) {
  return state === 'failed' || state === 'closed';
}
