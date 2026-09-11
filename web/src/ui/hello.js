// web/src/ui/hello.js — HELLO payload helpers.
//
// The agent sends {"name":"…"} on tmux/plain shells and
// {"name":"…","engine":"herdr"} on a herdr PTY. Old clients ignore unknown
// fields. A later rename HELLO may omit engine; we must not wipe the one
// we already learned.

export function applyHello(machine, meta) {
  if (!machine) return machine;
  if (!meta || typeof meta !== 'object') return machine;
  const next = { ...machine };
  if (typeof meta.name === 'string' && meta.name) next.name = meta.name;
  if (typeof meta.engine === 'string' && meta.engine) next.engine = meta.engine;
  return next;
}

export function hideTmuxStrip(engine) {
  return engine === 'herdr';
}
