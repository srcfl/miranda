# Deploying `tr-signal` (AWS Lightsail + Cloudflare)

The signaling server is a tiny stateless Go binary. It runs on a small Lightsail
instance behind Cloudflare, which provides TLS (so browsers/CLIs use
`wss://relay.sourceful-labs.net`). **The server only ever sees SDP / `roomID` /
ciphertext — terminal data flows peer-to-peer (Noise), never through it.**

## What is deployed (2026-06-06)

| Thing | Value |
|---|---|
| Lightsail instance | `tr-signal`, region `eu-north-1` (Stockholm), `ubuntu_24_04`, `nano_3_0` (~$5/mo) |
| Static IP | `16.171.89.172` (Lightsail `tr-signal-ip`) |
| Service | systemd `tr-signal.service`, listens on `:80`, user `trsignal` + `CAP_NET_BIND_SERVICE` |
| Open ports | 22 (SSH), 80 (HTTP — Cloudflare origin) |
| Health | `curl http://16.171.89.172/healthz` → 200 |

Architecture: client/agent `wss://relay.sourceful-labs.net` → **Cloudflare** (TLS
termination, WebSocket proxy) → origin `http://16.171.89.172:80` → `tr-signal`.

## Cloudflare setup (manual — do this in the `sourceful-labs.net` dashboard)

1. **DNS** → add a record:
   - Type `A`, Name `relay`, IPv4 `16.171.89.172`, **Proxied** (orange cloud).
2. **SSL/TLS → Overview** → encryption mode **Flexible** (Cloudflare ⇄ origin over
   HTTP:80; the origin has no cert). Cloudflare presents a valid public cert to
   clients, so `https://`/`wss://relay.sourceful-labs.net` just works.
3. **Network** → ensure **WebSockets** is **On** (default on).

Then verify: `curl https://relay.sourceful-labs.net/healthz` → 200.

> Hardening (optional, later): switch to **Full (strict)** by generating a
> Cloudflare **Origin CA** cert in the dashboard and terminating TLS on the origin
> (e.g. Caddy in front of `tr-signal`). Flexible is fine to start because the data
> plane is already E2E (Noise); the origin only sees signaling SDP.

## Using the deployed server

```bash
# on each machine:
tr-agent pair --signal https://relay.sourceful-labs.net
tr-agent up   --signal https://relay.sourceful-labs.net --stun stun:stun.l.google.com:19302 &
# on the client:
trm pair <code>
trm attach <machine> --stun stun:stun.l.google.com:19302
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

## Recreate from scratch

The instance was created with:

```bash
aws lightsail create-instances --region eu-north-1 --instance-names tr-signal \
  --availability-zone eu-north-1a --blueprint-id ubuntu_24_04 --bundle-id nano_3_0
aws lightsail allocate-static-ip --region eu-north-1 --static-ip-name tr-signal-ip
aws lightsail attach-static-ip   --region eu-north-1 --static-ip-name tr-signal-ip --instance-name tr-signal
```

Then provision with `redeploy.sh` (uploads the binary + installs
`tr-signal.service`, included here for reference).

## TURN (future, for symmetric-NAT fallback)

Not deployed yet. Add a `coturn` (on this box or another) with long-term creds,
open UDP 3478 + the relay port range in the Lightsail firewall, and pass
`--turn turn:relay.sourceful-labs.net:3478 --turn-user … --turn-pass …` to
`tr-agent up` and `trm`. Noise keeps the relay blind even when used.

## Cost

`nano_3_0` ≈ $5/mo; static IP is free while attached. Tear down:
`aws lightsail delete-instance --region eu-north-1 --instance-name tr-signal` and
`aws lightsail release-static-ip --region eu-north-1 --static-ip-name tr-signal-ip`.
