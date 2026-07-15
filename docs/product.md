# Product thesis: terminal continuity

## One sentence

**Miranda lets one person leave a device and continue the same live terminal on
another, using a passkey and an end-to-end-encrypted connection to machines they
own.**

## The niche

Miranda is not “SSH with different crypto” and not “a smaller VPN.” Its category is
**private terminal continuity**.

The initial user is a developer who has long-running terminal work — especially AI
coding agents, builds, data jobs, or debugging sessions — distributed across a
laptop, workstation, home server, or cloud box. The pain is not merely logging in.
It is finding the right live session and resuming it safely from the device currently
in hand.

`tmux` solves process and terminal persistence on one host. Miranda supplies the
missing cross-device layer:

```text
passkey identity → paired machines → encrypted discovery → safe reachability → tmux continuity
```

That makes “P2P tmux” a useful shorthand but an incomplete product definition.
Miranda is the secure continuity fabric around tmux.

## The product promise

> Leave your desk. Keep your terminal.

The ideal moment is: a developer starts an agent or build on a workstation, walks
away, opens a phone, and sees the same terminal exactly where it was — without
remembering a hostname, moving a key, joining a subnet, or exposing other services.

## Scope discipline

The first excellent product does five things:

1. pairs one owner to one machine with an understandable visual trust ceremony;
2. keeps a persistent tmux terminal reachable without inbound port forwarding;
3. discovers the owner's online machines without revealing their records to the
   relay;
4. reconnects cleanly across sleep and network changes;
5. makes switching among several live machines fast on desktop and mobile.

Everything else must justify the security and UX cost.

## Explicit non-goals

- general network or subnet access;
- arbitrary TCP forwarding;
- remote desktop;
- file synchronization;
- shared organizational bastions, RBAC, or session recording;
- SSH protocol compatibility;
- building a new terminal multiplexer instead of using tmux.

These may be adjacent markets, but absorbing them would destroy the narrow
capability users can understand and safely grant.

## Why it can spread

The shareable story is a visible before/after, not a cryptography diagram:

- before: “Which box was that agent running on? Is the VPN on? Where is my key?”
- after: open Miranda, tap the machine, continue typing.

The security story supports that magic: one passkey, one pairing comparison, no
network-wide access, and a relay that cannot read terminal content. The product
should lead with continuity and prove security immediately behind it.

## Product principles

- **Terminal, not network.** Grant the smallest capability that solves the job.
- **Pair deliberately; reconnect effortlessly.** Friction belongs at the trust
  decision, not every session.
- **Targets are expendable.** Compromising one agent must not reveal the owner root
  or authorize other machines.
- **The relay is coordination, never trust.** It may know where; it must never know
  what was typed.
- **Use proven engines.** tmux owns persistence; standard Noise patterns and mature
  primitives own cryptography.
- **Honest failure beats invisible downgrade.** Missing signatures, entropy,
  capabilities, or release verification fail closed with a useful explanation.

## Near-term bar for “awesome”

- a first-run path that reaches a terminal in under a minute;
- reliable browser passkeys on the supported browser matrix;
- reconnect that feels instant after phone sleep or Wi-Fi/cellular changes;
- clear online/offline/retrying states and actionable diagnostics;
- machine rename and a clearer retirement/re-pair flow around signed revocation;
- an independent protocol/implementation audit of the published threat model;
- measured success across home NAT, office NAT, cellular, and TURN fallback.

The north-star metric is not registrations. It is **successful continuation**: the
percentage of attempts where a user reaches the intended existing session quickly
enough that Miranda feels like one terminal spanning all their devices.
