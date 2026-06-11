# Deploying `mir-signal` (AWS Lightsail + Cloudflare)

The signaling server is a tiny stateless Go binary. It runs on a small Lightsail
instance behind Cloudflare, which provides TLS for the browser SPA at
`https://term.sourceful-labs.net` and signaling at
`wss://relay.sourceful-labs.net` (or any other proxied hostname routed to this
origin). **The server only ever sees SDP / `roomID` / ciphertext — terminal data
flows peer-to-peer (Noise), never through it.**

## What is deployed (target state)

This table is the **target state** the automation converges the box to — not a
live snapshot. The running instance was first provisioned under the pre-rename
`tr-signal` names; `redeploy.sh` performs the cutover (see "Cutover gotchas"
below). After a successful `redeploy.sh` the box matches this table.

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
`'self'` only — set the relay origins explicitly so the SPA can reach signaling.

Use the **repeatable `--csp-connect-src` flag** in the systemd unit, one origin
per occurrence (this is what `deploy/lightsail/mir-signal.service` ships):

```ini
ExecStart=/usr/local/bin/mir-signal --addr :80 --webroot /opt/mir-web \
  --csp-connect-src "'self'" \
  --csp-connect-src https://relay.sourceful-labs.net \
  --csp-connect-src wss://relay.sourceful-labs.net \
  --csp-connect-src https://term.sourceful-labs.net \
  --csp-connect-src wss://term.sourceful-labs.net
```

> **Do NOT** put this in `MIR_CSP_CONNECT_SRC` inside `/etc/mir-signal.env`. The
> systemd `EnvironmentFile` parser corrupts a value whose first token is
> single-quoted — `MIR_CSP_CONNECT_SRC='self' https://…` arrives as
> `selfhttps://…` (quotes + leading space eaten). The repeatable flag avoids
> this; the binary joins the flag tokens verbatim, so keep `'self'`'s quotes
> (and note the `"'self'"` double-quote wrapping needed for systemd's own
> ExecStart parser — see the unit file's comment). See "Cutover gotchas" below.

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

The browser owner key uses WebAuthn `prf` with RP ID `term.sourceful-labs.net` —
the **exact app host**, not the parent domain. The owner passkey is therefore
bound to that single origin, and sibling `*.sourceful-labs.net` subdomains
(including `relay.sourceful-labs.net`) are **outside** the owner-key trust
boundary: a passkey scoped to `term.sourceful-labs.net` cannot be exercised from
another subdomain.

Rules:

- Keep the RP ID at the exact app host. Do **not** widen it to the registrable
  parent `sourceful-labs.net` to "share" a passkey across subdomains — that would
  pull every such subdomain into the trust boundary.
- Serve the SPA (the only origin that runs a passkey ceremony for this RP ID)
  only from `term.sourceful-labs.net`, and keep untrusted JavaScript off it.
- If the RP ID ever changes (different app host, or a deliberate move to the
  parent domain), plan for passkey re-enrollment — existing credentials are
  scoped to the old RP ID and will not carry over.

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
mir pair --signal https://relay.sourceful-labs.net
mir up   --signal https://relay.sourceful-labs.net --stun stun:stun.l.google.com:19302 &
# on the client:
mir pair <code>
mir attach <machine> --stun stun:stun.l.google.com:19302
```

`--stun` (a public STUN server) lets peers discover their reflexive candidates for
real NAT traversal. For symmetric NATs that won't hole-punch, add a TURN relay
later (see below) and pass `--turn …`.

## Redeploy / update the binary

```bash
# from the repo root. Keep the SSH key OUTSIDE the repo tree (see below):
HOST=16.171.89.172 KEY=~/.ssh/mir-signal.pem ./deploy/lightsail/redeploy.sh
```

`redeploy.sh` verifies the binary's sha256 on the box before installing it as
root, so a swapped artifact in the shared `/tmp` is rejected.

**SSH key handling.** Store the key under `~/.ssh/` (mode 600) — not in the repo
working tree, where one stray `git add -f`, a `tar` of the directory, or a backup
tool would leak it (it is `.gitignore`d, which only stops accidental `git add`).
Prefer a **dedicated** key pair for this host over the Lightsail account default
key pair, so its blast radius is one box rather than the whole region:

```bash
# create a dedicated key, import its public half to Lightsail, attach on (re)create:
ssh-keygen -t ed25519 -f ~/.ssh/mir-signal -N '' -C mir-signal-deploy
aws lightsail import-key-pair --region eu-north-1 \
  --key-pair-name mir-signal --public-key-base64 "$(base64 < ~/.ssh/mir-signal.pub)"
# then use KEY=~/.ssh/mir-signal with redeploy.sh
```

If you must use the region default key pair, still write it to `~/.ssh/` (mode
600), never into `deploy/lightsail/`.

### One-time migration: `tr-signal` → `mir-signal`

`redeploy.sh` now performs the cutover automatically and idempotently, **before**
it enables `mir-signal`:

- stops + disables any old `tr-signal.service`, removes
  `/etc/systemd/system/tr-signal.service` and `/opt/tr-web`, then
  `daemon-reload`s — each step is a no-op on subsequent runs;
- migrates the TURN secret: if `/etc/mir-signal.env` is absent and
  `/etc/tr-signal.env` exists, it derives the former by renaming
  `TR_TURN_SECRET=` → `MIR_TURN_SECRET=` (mode 600). It never clobbers an
  existing `/etc/mir-signal.env`, so a rotated secret survives a re-run.

So a normal `redeploy.sh` run leaves the box in the target state with no manual
cleanup. The old `trsignal` **system user** is intentionally left in place (it is
harmless and removing it could orphan file ownership); drop it by hand if you want
a fully clean box:

```bash
ssh tr-signal 'sudo userdel trsignal 2>/dev/null; echo done'
```

(The AWS Lightsail instance + static IP keep their legacy names `tr-signal` /
`tr-signal-ip` — those only change on a full recreate.)

### Cutover gotchas / known traps

Hard-won from real incidents migrating the live box off the pre-rename setup.
The relay binary was updated in coordination with these scripts; this section
assumes that build is deployed.

- **EnvironmentFile eats a leading single-quoted CSP token.** Setting
  `MIR_CSP_CONNECT_SRC='self' https://…` in `/etc/mir-signal.env` does **not**
  work: systemd's `EnvironmentFile` parser strips the quotes and the leading
  space, so the binary receives `selfhttps://…` and the SPA's `connect-src`
  silently breaks (browser blocks every signaling connection). **Use the
  repeatable `--csp-connect-src` flag** in `ExecStart` instead (see "SPA security
  headers" above and the shipped `mir-signal.service`). The binary joins those
  tokens verbatim, so keep `'self'`'s quotes — and wrap it as `"'self'"` so
  systemd's *own* ExecStart quoting delivers the literal `'self'` token.
- **A missing TLS cert used to crash-loop the relay.** The unit passes
  `--tls-addr :443 --tls-cert … --tls-key …` for an eventual Cloudflare Full
  (strict) cutover, but the box currently runs Cloudflare **Flexible** with no
  origin cert. The binary now `os.Stat`-gates the TLS branch: if the cert/key
  files are absent it logs a warning and serves HTTP-only instead of
  `log.Fatal`-ing the process. So the `--tls-*` flags are safe to leave in; to
  actually enable HTTPS later, just drop the PEMs into `/etc/ssl/mir-signal/` and
  restart. (Before the gate, a stray `--tls-*` on a no-cert box meant
  `Restart=always` + instant fatal = a tight crash loop.)
- **`TR_TURN_SECRET` → `MIR_TURN_SECRET` rename.** The TURN shared secret env var
  was renamed with the `tr-` → `mir-` cutover. `redeploy.sh` migrates an existing
  `/etc/tr-signal.env` automatically (see above); `deploy/turn/setup-coturn.sh`
  and `mir-signal.service` already use the new name. If you wrote the secret by
  hand under the old name, rename the key (and the file) to match, or
  `/turn-credentials` will return 404 (TURN silently disabled).

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
