# Deploying `mir-signal` (AWS Lightsail + Cloudflare)

The signaling server is a tiny stateless Go binary. It runs on a small Lightsail
instance behind Cloudflare, which provides TLS for the browser SPA at
`https://term.sourceful-labs.net` and signaling at
`wss://relay.sourceful-labs.net` (or any other proxied hostname routed to this
origin). **The server only ever sees SDP / `roomID` / ciphertext — terminal data
flows peer-to-peer (Noise), never through it.**

## What is deployed (2026-06-06)

| Thing              | Value                                                                                                                      |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------- |
| Lightsail instance | `tr-signal` (legacy AWS name; renamed only on recreate), region `eu-north-1` (Stockholm), `ubuntu_24_04`, `nano_3_0` (~$5/mo) |
| Static IP          | `16.171.89.172` (Lightsail `tr-signal-ip`)                                                                                 |
| Service            | systemd `mir-signal.service`, listens on `:80`, user `mirsignal` + `CAP_NET_BIND_SERVICE`, serves `/opt/mir-web` when present |
| Open ports         | 22 (SSH), 80 (HTTP — Cloudflare origin); TURN ports only when enabled                                                      |
| Health             | `curl http://16.171.89.172/healthz` → 200                                                                                  |

Architecture: browser `https://term.sourceful-labs.net` and client/agent
`wss://relay.sourceful-labs.net` -> **Cloudflare** (TLS termination, WebSocket
proxy) -> origin `http://16.171.89.172:80` -> `mir-signal`.

## Cloudflare setup (manual — do this in the `sourceful-labs.net` dashboard)

1. **DNS** → add a record:
   - Type `A`, Name `term`, IPv4 `16.171.89.172`, **Proxied** (orange cloud).
   - Type `A`, Name `relay`, IPv4 `16.171.89.172`, **Proxied** (orange cloud).
2. **SSL/TLS → Overview** → encryption mode **Flexible** (Cloudflare ⇄ origin over
   HTTP:80; the origin has no cert). Cloudflare presents a valid public cert to
   clients, so `https://term.sourceful-labs.net` and
   `wss://relay.sourceful-labs.net` just work.
3. **Network** → ensure **WebSockets** is **On** (default on).

Then verify:

```bash
curl https://term.sourceful-labs.net/healthz
curl https://relay.sourceful-labs.net/healthz
```

> Hardening (optional, later): switch to **Full (strict)** by generating a
> Cloudflare **Origin CA** cert in the dashboard and terminating TLS on the origin
> (e.g. Caddy in front of `mir-signal`). Flexible is fine to start because the data
> plane is already E2E (Noise); the origin only sees signaling SDP.

## Live security hardening checklist

Apply this checklist to every Cloudflare hostname that routes to this
`mir-signal` origin, currently `term.sourceful-labs.net` and
`relay.sourceful-labs.net`.

### Cloudflare rate limits

Create Cloudflare WAF rate limiting rules for these public paths. Cloudflare can
match `http.request.uri.path`; for WebSocket paths it only evaluates the initial
HTTP 101 upgrade request, not later WebSocket frames.

References:

- <https://developers.cloudflare.com/waf/rate-limiting-rules/>
- <https://developers.cloudflare.com/network/websockets/>
- <https://developers.cloudflare.com/ruleset-engine/rules-language/fields/reference/http.request.uri.path/>

Use this shared hostname guard in each expression:

```text
http.host in {"term.sourceful-labs.net" "relay.sourceful-labs.net"}
```

Starting rules:

| Endpoint       | Expression suffix                                  |             Starting limit | Action               |
| -------------- | -------------------------------------------------- | -------------------------: | -------------------- |
| TURN creds     | `and http.request.uri.path eq "/turn-credentials"` |  30 requests / minute / IP | Block for 10 minutes |
| Pair bridge    | `and http.request.uri.path eq "/pair"`             |  20 upgrades / minute / IP | Block for 10 minutes |
| Browser attach | `and http.request.uri.path eq "/attach"`           | 120 upgrades / minute / IP | Block for 10 minutes |
| Agent signal   | `and http.request.uri.path eq "/agent/signal"`     |  60 upgrades / minute / IP | Block for 10 minutes |

Rollout steps:

1. Deploy each rule in Log mode first if the current Cloudflare plan supports it;
   otherwise deploy during a low-traffic window and watch Cloudflare Security
   Events.
2. Use IP as the base characteristic. If legitimate users sit behind shared NAT,
   raise the limit or use Cloudflare's NAT-aware characteristic if the plan has it.
3. Keep `/turn-credentials` tighter than the WebSocket paths: it is CORS-open and
   directly grants relay bandwidth.
4. Review the first 24 hours of events, then either lower noisy limits or document
   the observed baseline here.

### TURN TTL and abuse monitoring

`/turn-credentials` returns coturn REST credentials only when both
`MIR_TURN_SECRET` and `--turn-url` are configured. The server currently issues a
12-hour TTL (`ttl: 43200`), and coturn accepts the username until its embedded
expiry.

Checks:

```bash
curl -s https://relay.sourceful-labs.net/turn-credentials
sudo journalctl -u mir-signal --since "1 hour ago"
sudo journalctl -u coturn --since "1 hour ago"
```

Expected states:

- `404 turn not configured`: TURN is disabled or the shared secret is absent.
- JSON with `ttl: 43200`: TURN credentials are live; monitor bandwidth and logs.

Operational thresholds:

- Investigate any sustained `/turn-credentials` spike above the Cloudflare
  baseline, especially from many IPs or one ASN.
- Investigate coturn allocation churn, repeated auth failures, or relay traffic
  that does not line up with expected user testing.
- Keep the relay UDP port range narrow (`49160-49200` in
  `deploy/turn/setup-coturn.sh`) and do not broaden it without a capacity reason.

Emergency disable/rotate:

```bash
sudo sed -i.bak '/^MIR_TURN_SECRET=/d' /etc/mir-signal.env
sudo systemctl restart mir-signal
sudo systemctl stop coturn
```

If TURN must remain enabled, rotate the shared secret in `/etc/mir-signal.env`,
update `/etc/turnserver.conf`, then restart both services. Old derived credentials
remain valid until their embedded expiry, so keep the Cloudflare
`/turn-credentials` rule active during the rotation window.

### SPA security headers

When `mir-signal --webroot /opt/mir-web` serves the browser SPA, the served
JavaScript is a client trust root. `mir-signal` now emits these headers itself
(see `setStaticSecurityHeaders`), but the `connect-src` allow-list defaults to
`'self'` only — set the relay origins explicitly so the SPA can reach signaling:

```bash
# in /etc/mir-signal.env
MIR_CSP_CONNECT_SRC="'self' https://term.sourceful-labs.net wss://term.sourceful-labs.net https://relay.sourceful-labs.net wss://relay.sourceful-labs.net"
```

You may still layer the equivalent headers at Cloudflare (belt and suspenders);
either way the policy below is the target. Apply it before using the browser
against real machines:

```text
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' https://term.sourceful-labs.net wss://term.sourceful-labs.net https://relay.sourceful-labs.net wss://relay.sourceful-labs.net; img-src 'self' data: blob:; media-src 'self' blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'; upgrade-insecure-requests
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Permissions-Policy: camera=(self), microphone=(), geolocation=(), payment=(), usb=(), serial=()
```

Only enable `Strict-Transport-Security: max-age=31536000; includeSubDomains`
after every required `sourceful-labs.net` subdomain is HTTPS-ready. Add `preload`
only after confirming the whole registrable domain is ready for browser preload.

Verify:

```bash
curl -I https://term.sourceful-labs.net/
curl -I https://term.sourceful-labs.net/src/app.js
```

### WebAuthn RP ID boundary

The browser owner key uses WebAuthn `prf` with RP ID `sourceful-labs.net`. This
lets one synced passkey work across `term`, `relay`, and future trusted
subdomains, but it also makes the registrable domain the trust boundary.

Rules:

- Do not delegate arbitrary `sourceful-labs.net` subdomains to third parties.
- Do not host untrusted JavaScript on any `sourceful-labs.net` subdomain that can
  request a passkey ceremony for this RP ID.
- If the product moves to a separate domain or a narrower RP ID such as
  `term.sourceful-labs.net`, plan for passkey re-enrollment because existing
  credentials are scoped to the old RP ID.

### Pairing safety-number runbook

For every real machine pairing:

1. Start pairing on the agent and scan/paste the generated code in the browser or
   CLI.
2. Read the agent-side `safety number: xxxx-xxxx-xxxx-xxxx` out of band.
3. Compare it with the browser/CLI safety number before trusting the new machine.
4. Abort and delete the pairing if the numbers differ. A mismatch means the
   pairing token was observed or the pairing path was intercepted.

## Using the deployed server

```bash
# on each machine:
mir-agent pair --signal https://relay.sourceful-labs.net
mir-agent up   --signal https://relay.sourceful-labs.net --stun stun:stun.l.google.com:19302 &
# on the client:
mir pair <code>
mir attach <machine> --stun stun:stun.l.google.com:19302
```

`--stun` (a public STUN server) lets peers discover their reflexive candidates for
real NAT traversal. For symmetric NATs that won't hole-punch, add a TURN relay
later (see below) and pass `--turn …`.

## Redeploy / update the binary

```bash
# from the repo root, with the SSH key saved at deploy/lightsail/key.pem (chmod 600):
HOST=16.171.89.172 KEY=deploy/lightsail/key.pem ./deploy/lightsail/redeploy.sh
```

The SSH key is the Lightsail default key pair for the region:
`aws lightsail download-default-key-pair --region eu-north-1 --query privateKeyBase64 --output text > deploy/lightsail/key.pem && chmod 600 deploy/lightsail/key.pem`
(do NOT commit it — it is gitignored).

### One-time migration: `tr-signal` → `mir-signal`

`redeploy.sh` installs the new `mir-signal` service/user/paths but does **not**
remove the old `tr-signal` ones, so the first run leaves both. After confirming the
new service is healthy, clean up the legacy unit on the box:

```bash
ssh tr-signal 'sudo systemctl disable --now tr-signal.service; \
  sudo rm -f /etc/systemd/system/tr-signal.service; sudo systemctl daemon-reload; \
  sudo rm -rf /opt/tr-web; sudo userdel trsignal 2>/dev/null; \
  [ -f /etc/tr-signal.env ] && sudo mv /etc/tr-signal.env /etc/mir-signal.env; \
  sudo systemctl restart mir-signal; systemctl is-active mir-signal'
```

(The AWS Lightsail instance + static IP keep their legacy names `tr-signal` /
`tr-signal-ip` — those only change on a full recreate.)

## Recreate from scratch

The instance was created with:

```bash
aws lightsail create-instances --region eu-north-1 --instance-names tr-signal \
  --availability-zone eu-north-1a --blueprint-id ubuntu_24_04 --bundle-id nano_3_0
aws lightsail allocate-static-ip --region eu-north-1 --static-ip-name tr-signal-ip
aws lightsail attach-static-ip   --region eu-north-1 --static-ip-name tr-signal-ip --instance-name tr-signal
```

Then provision with `redeploy.sh` (uploads the binary + installs
`mir-signal.service`, included here for reference).

## TURN fallback

TURN is optional and only active when `coturn` is running, the Lightsail firewall
allows 3478 TCP/UDP plus the configured UDP relay range, and `/etc/mir-signal.env`
contains `MIR_TURN_SECRET`. `mir-signal.service` already includes `--turn-url`;
without the shared secret `/turn-credentials` returns 404 and clients continue
STUN-only. Noise keeps the relay blind even when TURN is used, but TURN still
consumes relay bandwidth, so keep the hardening checklist above in force.

## Cost

`nano_3_0` ≈ $5/mo; static IP is free while attached. Tear down:
`aws lightsail delete-instance --region eu-north-1 --instance-name tr-signal` and
`aws lightsail release-static-ip --region eu-north-1 --static-ip-name tr-signal-ip`.
