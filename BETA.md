# Miranda beta

Miranda is the reach layer for persistent terminals: `tmux` keeps your session
alive on the machine, and Miranda gets you to that machine from the device in
your hand. Pair a phone or laptop with a passkey, and the machine's `tmux`
sessions stay reachable through a blind relay that never sees plaintext, over an
end-to-end-encrypted connection. Leave your desk. Keep your terminal.

This is a beta. Read the gaps below before you rely on it.

## What works today

- `mir up` on a fresh machine offers to install `tmux`, then prints a pairing
  QR and safety number itself — no separate `mir pair` step on first run.
- Scan the QR (or open the web app) on a phone or laptop, compare the safety
  number, and the terminal is live.
- Reconnect after sleep or a network change targets under 3 seconds, with a
  status pill that shows online, reconnecting, or offline honestly, never a
  frozen spinner.
- The web app holds up to three machines warm at once; switching between them
  is instant and does not drop either session.
- The tmux window strip updates the moment a window changes, pushed from tmux
  hooks, not a poll.
- Renaming a machine, from the CLI or the web app, propagates to every device
  through the encrypted registry.
- The CLI attaches several machines at once (`mir attach a b c`) and switches
  focus with `Ctrl-O` then a number.
- Share a terminal with someone for a bounded time: `mir share <machine>`
  mints an invite (read-only by default, write only by explicit heavy consent,
  1 h default, 24 h cap); the guest joins with `mir join <code>` or the web
  link and the share expires on its own. Read-only guests see one pane and
  cannot type; write access is full control, and the prompt says so. See
  [SECURITY.md](SECURITY.md#session-sharing).
- Machine revocation, native OS-keychain storage for the owner root, signed
  and reproducible releases, and the shared Go/JavaScript cryptography vectors
  all carry over from v0.7.0. See [SECURITY.md](SECURITY.md) for the exact
  guarantees.

## Known gaps

- **Passkey/browser matrix.** No published table of which browsers and
  authenticators (Safari, Chrome, Firefox, iCloud Keychain, 1Password, and so
  on) are tested and supported. Most combinations work; none are certified
  yet.
- **Real-network numbers.** Lab measurements exist — a Docker NAT matrix
  ([netsim/results/results.md](netsim/results/results.md)) shows attach from
  12 ms on a LAN to ~1.0 s over TURN, and resume after a network flip in
  ~2.4 s direct / ~2.8 s over TURN — but nothing is published yet from real
  home, office, or cellular networks.
- **External audit.** Miranda has not had an independent security audit. A
  scope document exists ([docs/audit-scope.md](docs/audit-scope.md)), but no
  audit has been commissioned or completed.
- **Sharing v2 items.** A read-only share mirrors one pane and does not
  follow the owner's window switches; revocation reaches the machine only
  when it is online (the 24 h cap is the backstop); and shares are minted
  from the CLI only — phone minting comes later.
- **No telemetry.** Miranda does not phone home, by design. That means we
  cannot see how the beta is going without your reports — see below.

## Install and join

```bash
curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | MIR_CHANNEL=beta sh
```

`MIR_CHANNEL=beta` installs the newest beta build (a plain install gives the
last stable release, which predates everything this page describes). A beta
build keeps itself on the beta channel: `mir update` follows prereleases.

On the machine whose terminal should stay reachable, run:

```bash
mir up
```

On first run this offers to install `tmux` if it is missing, then shows a QR
code and a six-group safety number. Scan the QR with the Miranda web app (or
open it directly and pair from there), compare the safety number on both
screens, and confirm. The terminal is now reachable from any device paired to
the same owner.

Full install and self-hosting details are in the [README](README.md#install).

## Report a problem

File an issue: <https://github.com/srcfl/miranda/issues/new/choose>. Pick
**Bug report** for something broken, or **Beta feedback** for anything that
felt slow, confusing, or wrong even if nothing failed outright.

A good report includes:

- your platform (macOS or Linux, and the version);
- browser and CLI version if the web app is involved;
- the output of `mir doctor --share` — the same checks as `mir doctor`,
  printed as a paste-safe report: no identities, machine names, file paths,
  or custom relay URLs;
- what you attempted;
- what happened;
- what you expected instead.

Ten spare minutes? Walk the [beta checklist](docs/beta-checklist.md) — with no
telemetry, those notes are the only way the beta gets measured.

Security issues go to security@sourceful-labs.net, not a public issue — see
[SECURITY.md](SECURITY.md).
