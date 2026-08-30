# Beta checklist

Miranda has no telemetry, so this walk-through is how the beta gets measured.
It takes about ten minutes. Note the numbers as you go and paste them into a
[beta feedback issue](https://github.com/srcfl/miranda/issues/new/choose) with
the output of `mir doctor --share`.

Each step maps to a release gate in the
[beta roadmap](superpowers/plans/2026-08-29-v0.8-beta-ux-roadmap.md).

## 1. First minute (gate: install → terminal under 60 s)

Start a timer. On a machine that has never run Miranda:

```bash
curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | MIR_CHANNEL=beta sh
mir up
```

Scan the QR with your phone, create the passkey, compare the safety number,
confirm. Stop the timer when the terminal is live.

**Note down:** total seconds; where you waited or hesitated; anything you had
to read twice.

## 2. Reconnect (gate: resume under 3 s)

With a session open on the phone: turn Wi-Fi off so it flips to cellular.
Watch the status pill.

**Note down:** roughly how long until the terminal was live again; whether the
pill told the truth the whole time; the `reconnected in …` line if you were
attached from the CLI.

## 3. Warm switching

Run `mir up` on a second machine (pair it when asked). Open both from one
browser tab and switch between them a few times.

**Note down:** whether switching felt instant; whether either session dropped;
what the machine strip showed while a machine was reconnecting.

## 4. Rename

Rename a machine from the phone (✎ in the terminal view). Look at another
device.

**Note down:** whether the new name reached every device without a reload.

## 5. Retire and come back

Retire the second machine from the machine list. Read the confirmation before
you tap it. Then bring the machine back: `mir up` on it, pair fresh.

**Note down:** whether the confirmation told you what you needed to know
before you tapped; anything surprising on the way back.

## 6. Share a terminal

On the laptop: `mir share <machine>` (defaults: read-only, 1 h). On the phone
— or a second person's device — open the invite link, read the safety number
aloud, and have the minter approve. Watch the share open read-only, then let
it expire (or `mir share revoke <id>`).

**Note down:** how long mint → joined took; whether the read-only view showed
live output; whether typing into it did anything (it must not); what happened
at expiry or revoke — the honest end line, or anything confusing.

## 7. Your setup

**Note down:** phone model + browser; laptop browser; passkey provider
(iCloud Keychain, 1Password, …); network (home, office, cellular). This feeds
the passkey/browser and NAT matrices, which are not published yet.
