// web/src/rp.js — WebAuthn RP ID scoping.
//
// Production passkeys are bound to the exact terminal app host. Do not widen
// this to the parent domain; sibling subdomains must not share this trust root.
export const PRODUCTION_RP_ID = 'term.sourceful-labs.net';

export function resolveRPID(hostname) {
  return hostname === 'localhost' ? 'localhost' : PRODUCTION_RP_ID;
}
