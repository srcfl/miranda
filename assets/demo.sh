#!/usr/bin/env bash
# Illustrative Miranda demo for the README GIF — authored output, not a live
# capture, so the story stays tight and reproducible.  Rendered by
# assets/demo.tape with charmbracelet/vhs:
#
#   vhs assets/demo.tape        # run from the repo root
#
# Honesty rule for this file: every line it prints is a line the current `mir`
# prints.  The overview screen is go/internal/cli/overview_model.go's Render()
# output; the attach banner is overview.go's; the `mir share` block — header,
# QR, both invite lines, the wait line — was captured verbatim from a real run
# against relay.sourceful-labs.net.  That invite has long since expired.  The
# tmux screen and the shell prompt are the user's own programs, not mir's.
set -u

C=$'\033[0m'; DIM=$'\033[2m'; B=$'\033[1m'; MG=$'\033[35m'
STATUS=$'\033[30;42m'                 # tmux's default status bar: black on green
ALT_ON=$'\033[?1049h\033[?25l'        # overview.go: altScreenOn
ALT_OFF=$'\033[?25h\033[?1049l'       # overview.go: altScreenOff
CLS=$'\033[H\033[2J'                  # overview.go: clearScreen
PR="${DIM}~${C} ${MG}\$${C} "         # the demo user's shell prompt

cmd() { printf "%b%b%b\n" "$PR" "$B" "$1$C"; sleep 0.9; }

# ── the overview, exactly as overviewModel.Render() draws it ──────────────────
overview() {
  printf '%s%s' "$ALT_ON" "$CLS"
  printf '\r\n'
  printf ' %smir%s%s — your machines%s\r\n' "$B" "$C" "$DIM" "$C"
  printf '\r\n'
  printf ' ▸ ● workstation\r\n'
  [ "${1:-}" = "windows" ] && printf '     %s3 windows — agent, build, logs%s\r\n' "$DIM" "$C"
  printf '   ● builder\r\n'
  printf '   ○ pi\r\n'
  printf '\r\n'
  printf ' %senter attach · s share · r rename · x retire · q quit · ? help%s\r\n' "$DIM" "$C"
}

# ── what the far end paints once the data channel is up: plain tmux.  tmux
#    takes the alt screen itself, which is why detaching puts the banner back.
tmux_screen() {
  local rows cols left right
  rows=$(tput lines); cols=$(tput cols)
  left='[main] 0:agent* 1:build- 2:logs'
  right='"workstation" 15:42 30-Aug-26 '
  printf '%s%s' "$ALT_ON" "$CLS"
  printf 'workstation:~/src/ingest $ ./agent run --plan refactor.md\r\n'
  printf '[13:58:04]  step 4/9   rewrote loader.go, splitter.go\r\n'
  printf '[14:19:40]  step 5/9   go test ./... — 312 passed\r\n'
  printf '[15:41:57]  step 6/9   migrating fixtures … 41%%\r\n'
  printf '%s▉%s' "$B" "$C"
  printf '\033[?7l\033[%d;1H%s%s%*s%s%s\033[?7h' "$rows" "$STATUS" "$left" \
    "$(( cols - ${#left} - ${#right} ))" '' "$right" "$C"
}

printf '%s' "$CLS"   # wipe the `bash assets/demo.sh` invocation line
sleep 0.8

# 1. bare `mir` opens your machines.
cmd "mir"
overview
sleep 2.2

# 2. Enter attaches. overview.go leaves the alt screen, prints the banner on
#    stderr, then the machine's own tmux paints over it.
printf '%s' "$ALT_OFF"
printf '[mir] attached to workstation — Ctrl-O then d comes back to your machines\r\n'
sleep 0.7
tmux_screen
sleep 3.2

# 3. Ctrl-O d comes back: tmux gives the screen up, then the overview redraws.
#    The row now remembers what is running over there.
printf '%s' "$ALT_OFF"
overview windows
sleep 2.2

# 4. q quits to the shell; one invite, read-only, gone in an hour.
printf '%s' "$ALT_OFF"
cmd "mir share --ttl 1h workstation"
printf 'Share "workstation" — read-only access until 16:42.\n'
printf '\n  📱 Have your guest scan this:\n\n'
sleep 0.3
/bin/cat <<'QR'
█████████████████████████████████████████████████████
█████████████████████████████████████████████████████
████ ▄▄▄▄▄ ██▀█▀▄▄▄█▄▀▀▄▄▀▀ █ ▄    █▀ █▀▄█ ▄▄▄▄▄ ████
████ █   █ █▄ █▄▄███▄▄▄█▀  ▀█▀  █▄▀█▄██ ▀█ █   █ ████
████ █▄▄▄█ ██▀ █▄▄▀▄     ▄▄▄  ▄   ▄   ▄▄▄█ █▄▄▄█ ████
████▄▄▄▄▄▄▄█ ▀▄█▄▀ ▀▄▀ █ █▄█ ▀ █ █▄▀ █▄▀ █▄▄▄▄▄▄▄████
████   █▄▄▄▄ ██   ██ ▄▄▀  ▄▄▄██▄▄▄▄  ▄▄██▄▄▀▀█▄█▀████
████ █▀█▄▄▄▄▄  ▀████▀▀▀▄▄█▄▄ ▄▀▄▄▄█ ▀█▀ █▀██▄██ ▄████
████   ▀ ▄▄▀▀▄█▀█▄▄ █▄▄  ▀▀█▄██  █▄  ▄███▄▄ ▄▀█ ▄████
█████▄   █▄▄█▀▀ ▀█▄▀███ ███ ▄▀▀▀ ██▀▀██  █▀█▀█▄▄ ████
████ ▄█  █▄▄ ▀ ▀██▄▀▄█   ▀▄█▀██▄▄▀▄█ ████ ▄▄▄ ▀▄ ████
█████ █▄▀ ▄▀ ▀█▀▄██ ▄▄ ██▄██▄█ ▄▀▀█▀▄ ▀▀█▀▀▄▄▄▄ ▄████
████  ▄▄ ▄▄▄ ▄▀ ▄█ ██▄ █ ▄▄▄ ▄▄█▄ █▄ ▄▄▀ ▄▄▄ ▀█ ▄████
████▀▄▀▀ █▄█ ▄▄█▀ ▀▀▀█ ▄ █▄█ ▀  ▀▄▀▄ ▄▄  █▄█  █  ████
████▀██  ▄▄▄▄▀▀ ▄ █▄▄▄▄▄  ▄▄▄▄▄▀  █  ▄▄▀▄▄▄▄▄ ██ ████
████ ▄█▀▄█▄█▀▀▀ ▄▀ ██▀█▀ ▄██▄█▀ ███▀▄▀▀█▀ ▄▀▀▄█ ▄████
████ ▀█ █▀▄▀▄▀▀▄█▄▄▄█    █  ▄▄▄  ▄▄  ▄▄▀▀ ▀██ ▀▀▀████
████▀▄█▀ ▀▄▀█▄▄▄██▄▄▄▀▀█▀▀██▄ ▀▀▀▀▄ ▀▄▀██▄ █▄▀█ ▄████
████▄▀ ▀▀▀▄█▀███ █▄██ ▄▀  ▄█▄██▄▄▄█▄▄▀▄█▄ █▄▄▀▀  ████
█████▀▀▀ █▄▀▄█▄█▀    ▀██ █ ██▀▀▀▀█▀▀▄▀▀▄▀ ▄▄▄ ▄ ▄████
████▄██▄▄█▄▄  █▄▄   █  ▀ ▄▄▄ ▄▄  ██ ▄███ ▄▄▄ ▄▀█ ████
████ ▄▄▄▄▄ █  ▀█▄ ▀ █ ▄█ █▄█ █      ▀▄ ▄ █▄█  █▄ ████
████ █   █ █▄▀  ▄ ███▄▄▀ ▄▄▄▄▄█▄ ██ ▄▄▄ ▄ ▄▄ ██▄▄████
████ █▄▄▄█ █  ▄ ▄▄█▄▄▄▄▄▀█ ▀██▀ ███ ▀▀█▄ ▄█▄ ▄▄▀█████
████▄▄▄▄▄▄▄█▄█▄██▄▄▄▄▄▄█▄▄▄▄▄▄█▄▄▄█▄▄███▄██▄█▄█▄▄████
█████████████████████████████████████████████████████
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
QR
printf '\n  …or open: https://term.sourceful-labs.net/#join-eyJzIjoiaHR0cHM6Ly9yZWxheS5zb3VyY2VmdWwtbGFicy5uZXQiLCJ0IjoiYzRlNGJiODc5NWFjM2Y2NmMyODE3ZjM3NGNiYjY3ZDUifQ\n'
printf '  …or on the CLI:  mir join eyJzIjoiaHR0cHM6Ly9yZWxheS5zb3VyY2VmdWwtbGFicy5uZXQiLCJ0IjoiYzRlNGJiODc5NWFjM2Y2NmMyODE3ZjM3NGNiYjY3ZDUifQ\n'
printf '\nwaiting for your guest (5 min)…\n'
sleep 4
