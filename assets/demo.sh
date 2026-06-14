#!/usr/bin/env bash
# Illustrative Miranda demo for the README GIF — scripted (not a live capture) so the
# story is tight and reproducible. Rendered via assets/demo.tape with charmbracelet/vhs.
set -u

C=$'\033[0m'; DIM=$'\033[2m'; B=$'\033[1m'
G=$'\033[32m'; CY=$'\033[36m'; YL=$'\033[33m'; MG=$'\033[35m'; GY=$'\033[90m'
PR="${DIM}~${C} ${MG}\$${C} "        # local prompt
RP="${CY}macmini${C}:${DIM}~${C} ${MG}\$${C} "  # "remote" prompt

printf '\033[H\033[2J'   # wipe the `bash assets/demo.sh` invocation line — start clean

# cmd <text> — render a prompt and a typed command, with a beat before output.
cmd() { printf "%b%b%b\n" "$1" "$B" "$2$C"; sleep 0.55; }
out() { printf "%b\n" "$1"; sleep 0.18; }

sleep 0.4
cmd "$PR" "mir up"
out "  ${G}✓${C} serving ${B}macmini${C}  ${DIM}·${C} wallet ${YL}G4XC6h…k4kN${C}  ${DIM}·${C} relay.sourceful-labs.net"
out "  ${G}✓${C} LAN-direct on ${DIM}(mDNS + QUIC)${C}  ${DIM}·${C} persistent tmux  ${DIM}·${C} ${DIM}the relay never sees your shell${C}"
sleep 0.7
printf "\n%b\n\n" "${GY}── from your laptop, anywhere ───────────────────────────────${C}"
sleep 0.3

cmd "$PR" "mir list"
out "  ${MG}📣${C} new device ${B}\"macmini\"${C} joined your wallet"
out "  ${B}macmini${C}   ${DIM}a1b2c3d4…${C}   ${G}online${C} ${DIM}· LAN-direct${C}"
out "  ${B}linux${C}     ${DIM}d4e5f6a7…${C}   ${G}online${C} ${DIM}· relay${C}"
sleep 0.7

cmd "$PR" "mir attach macmini"
out "  ${DIM}[mir]${C} ${B}macmini${C} — peer-to-peer, ${CY}Noise-encrypted end-to-end${C}  ${DIM}(no SSH, no port-forward)${C}"
sleep 0.5
cmd "$RP" "uptime"
out " 14:32:07 up 3 days,  2:11,  load average: 0.11, 0.09, 0.05"
sleep 0.3
cmd "$RP" "whoami && hostname"
out "fredrik"
out "macmini"
sleep 0.4
printf "%b%b" "$RP" "${B}▉${C}"
sleep 1.6
