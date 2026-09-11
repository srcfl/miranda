// web/src/ui/hello.js — HELLO payload helpers.
//
// The agent sends {"name":"…"} on tmux/plain shells and
// {"name":"…","engine":"herdr"} on a herdr PTY. Old clients ignore unknown
// fields. A later rename HELLO may omit engine; we must not wipe the one
// we already learned.

export function applyHello(machine, meta) {
  if (!machine) return machine;
  if (!meta || typeof meta !== 'object') return machine;
  const name = typeof meta.name === 'string' && meta.name ? meta.name : '';
  const engine = typeof meta.engine === 'string' && meta.engine ? meta.engine : '';
  if (!name && !engine) return machine;
  const next = { ...machine };
  if (name) next.name = name;
  if (engine) next.engine = engine;
  return next;
}

export function hideTmuxStrip(engine) {
  return engine === 'herdr';
}
