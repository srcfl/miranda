// web/src/rp.js — WebAuthn RP ID scoping.
//
// Bind passkeys to the exact host serving this build. This keeps sibling origins
// isolated and makes a self-hosted Miranda deployment work on its own domain.
export function resolveRPID(hostname) {
  const host = String(hostname || '').trim().toLowerCase();
  if (!host || host.includes('/') || host.includes(':')) throw new Error('invalid WebAuthn RP host');
  return host;
}
