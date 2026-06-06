# terminal-relay — Plan 6b: the browser SPA (interactive)

> Built and validated **in a real browser** (Chrome), not headless — WebAuthn,
> WebRTC, and xterm.js can't be verified blind. Reuses the Plan-1/6a JS modules
> (`noise-kk.js`, `frame.js`, `owner.js`, `pairing/*`), all certified vs Go.

**Goal:** a static SPA (served at `term.sourceful-labs.net` later) where you open a
browser, authenticate with a passkey, and attach to a real shell on your machine
over a direct P2P WebRTC DataChannel with Noise inside. The "from my iPhone" dream.

## Milestones (incremental, each validated in Chrome)

1. **Foundation in-browser.** `index.html` with an import-map so the bare `@noble/*`
   imports in the existing modules resolve from a CDN (no bundler). A self-test page
   confirms `noise-kk` + `pairing` run in a real browser (not just Node).
2. **Attach (dev key).** `app.js`: signaling client (`/attach`), `RTCPeerConnection`
   offerer (non-trickle), Noise `KK` initiator over the DataChannel, xterm.js render
   + input + resize. Owner key + machine descriptor entered manually / dev key in
   localStorage. → a live shell in a browser tab against a local `tr-agent`.
3. **Pairing UI.** Read the token from the URL fragment (`#<code>`) or a field →
   `pairing/nnpsk0` initiator over `/pair` → store the machine; show the safety number.
4. **Passkey identity.** WebAuthn register/login with the `prf` extension →
   `owner.js deriveOwnerKey`. RP = the serving origin. Dev fallback: local key.
5. **Multi-machine + polish.** Machine list/picker, reconnect, mobile/iOS Safari.
6. **Serve + deploy.** Static hosting at `term.sourceful-labs.net` (Cloudflare Pages
   or the Lightsail box); RP-ID = `sourceful-labs.net`.

## Files

```
web/index.html              # SPA shell: import-map (@noble + xterm CDN), mount
web/src/app.js              # attach glue: signaling + RTCPeerConnection + Noise + xterm
web/src/browser/signal.js   # browser signaling client (offer/answer over WSS)
web/src/selftest.html       # milestone-1 in-browser crypto self-test
web/dev-serve.sh            # local static server (http://localhost — a WebAuthn secure context)
```

## Notes

- `@noble/*` bare specifiers resolve via an `importmap` to `esm.sh` (versions pinned
  to match `web/package.json`). Validate the exact subpaths in-browser.
- `http://localhost` is a WebAuthn secure context, so passkeys work in local dev
  (RP-ID `localhost`) — a disposable identity, per the spec's domain-change note.
- Signaling URL/STUN default to ours (see `internal/defaults`), overridable.
- This plan is executed interactively, validating each milestone in Chrome; it is
  not a headless TDD workflow.
