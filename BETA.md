# Miranda beta

Miranda lets you leave a desk and keep the same live terminal. Pair a phone or
laptop to a machine with a passkey, and `tmux` sessions on that machine stay
reachable through a blind relay that never sees plaintext, over an
end-to-end-encrypted connection.

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
  ([netsim/results/results.md](netsim/results/results.md)) shows attach in
  0.2–1.3 s and resume after a network flip in ~2.6 s direct / ~3.0 s over
  TURN — but nothing is published yet from real home, office, or cellular
  networks.
- **External audit.** Miranda has not had an independent security audit. A
  scope document exists ([docs/audit-scope.md](docs/audit-scope.md)), but no
  audit has been commissioned or completed.
- **No telemetry.** Miranda does not phone home, by design. That means we
  cannot see how the beta is going without your reports — see below.

## Install and join

```bash
curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | sh
```

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
