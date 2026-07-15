# Miranda production operations

This is the short incident and readiness runbook. Deployment-specific commands and
Cloudflare settings live in `deploy/lightsail/README.md`.

## Release-candidate acceptance

- `go test ./...`, `go test -race -count=1 ./...`, `npm test`, fuzz smoke, install
  tests, and `scripts/verify-reproducible.sh` are green on the release commit.
- `mir doctor` reports no blocking failures on every owner client and target.
- Relay origin uses Full (strict), the origin firewall accepts only the trusted
  proxy ranges, proxy headers are CIDR-gated, and `/revocations` is durable.
- Direct WebRTC, LAN QUIC, TURN fallback, Wi-Fi/cellular transition, browser sleep,
  agent restart, relay restart, and relay outage are exercised with real devices.
- A current revocation backup exists and restore has been rehearsed.

## Signals to monitor

`mir-signal` emits structured events suitable for journald aggregation:

- `agent_reject`, `agent_replaced`, `agent_flap`, `agent_gone`;
- `attach`, `attach_offline`, `attach_capacity`, `attach_revoked`;
- `rate_limit`, `machine_revoked`, `revocation_reject`,
  `revocation_persist_error`;
- periodic `stats` with live agents, proof entries, and revocation count.

Alert on any persistence error, repeated flap for one slot, sustained capacity/rate
events, unexplained revocation growth, origin requests outside the proxy network,
or TURN bandwidth that does not match active sessions. Do not log full owner IDs,
registration secrets, signed attach payloads, recovery phrases, SDP, or terminal
data.

## Compromised or lost target

1. From an uncompromised owner client, run `mir machine revoke NAME --yes` or use
   the browser revoke control.
2. Confirm local blocking and successful publication to every configured relay.
3. Confirm `machine_revoked` on the relay and that re-registration returns 410.
4. Disconnect any other already-established clients; the blind relay cannot kill
   a P2P session after signaling has completed. Isolate/reimage the target and
   rotate credentials available to its Unix account.
5. Re-pair only after the host is trusted again. Revocation itself is permanent for
   that owner/machine ID; a reinstalled target receives a new machine identity.

## Compromised owner root, passkey account, or web origin

Per-machine revocation is insufficient because the attacker can sign as the owner.
Take the web origin offline if implicated, preserve evidence, run
`mir identity rotate --yes` from a clean client, re-pair every target in person,
and revoke/remove the old passkey credential at its provider. Rotate deploy/release
credentials if the browser origin or CI was in scope. Publish an incident notice
only after a private vulnerability report and user remediation path exist.

## Relay incident

The relay is not a confidentiality authority, so fail closed without rotating
owner roots solely for relay compromise. Preserve logs and the signed revocation
file, disable TURN if it is being abused, rebuild from a known release, restore the
verified tombstones, and re-enable traffic behind edge limits. Rotate TURN and
deployment credentials. Registration secrets are machine-slot availability
credentials; re-pair/reinstall a target if its slot is actively being disrupted.

## Backup and restore

Back up only the relay's signed revocation file and deployment configuration.
Encrypted discovery and live registrations are soft state and republish after
restart. Stop `mir-signal`, restore `revocations.json` as mode `0600` owned by the
service user, then start it; startup signature verification is the restore gate.
Never “repair” a corrupt tombstone file by deleting entries. Restore a known-good
backup and investigate the corruption.
