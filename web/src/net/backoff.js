// backoff returns a full-jitter exponential delay in ms: a random value in
// [0, min(cap, base*factor**attempt)]. Full jitter (vs equal jitter) avoids a
// reconnect thundering-herd and tight loops on a flapping link. random is
// injectable so the policy is deterministically testable.
export function backoff(attempt, { base = 500, factor = 2, cap = 10000, random = Math.random } = {}) {
  const ceil = Math.min(cap, base * factor ** attempt);
  return Math.round(random() * ceil);
}
