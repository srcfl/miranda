// web/src/registry.js — discover your machines from the relay's encrypted device
// registry (B2). Mirrors go/internal/client/registry.go. The relay serves opaque
// blobs keyed by wallet; only a wallet-holder (registryKey) can open them, so a
// forged/garbage blob fails to open and is silently dropped. Discovery only — the
// Noise data plane and attach path are unchanged.
import { registryKey, openRecord } from './identity/registry.js';

const td = new TextDecoder();

function b64ToBytes(s) {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// decodeRegistry turns the relay's `[{machine_id, blob}]` into machines, dropping
// any blob that fails to open (a forgery, or one sealed under a different wallet).
// fallbackSignal is used when a record carries no signal_url of its own.
export function decodeRegistry(entries, secret, fallbackSignal) {
  const key = registryKey(secret);
  const out = [];
  for (const e of entries || []) {
    let rec;
    try {
      rec = JSON.parse(td.decode(openRecord(key, b64ToBytes(e.blob), e.machine_id)));
    } catch {
      continue; // forged / garbage / wrong machine_id — drop it
    }
    out.push({
      machine_id: e.machine_id,
      name: rec.name,
      host_pub: rec.host_pub,
      signal: rec.signal_url || fallbackSignal,
    });
  }
  return out;
}

// fetchMachines GETs the wallet's registry from `origin` and decodes it. Best-effort:
// any failure (relay down, not served same-origin, bad JSON) returns [] so the caller
// falls back to the locally-stored machine list without surfacing noise.
export async function fetchMachines(origin, wallet, secret) {
  try {
    const url = origin.replace(/\/$/, '') + '/registry?wallet=' + encodeURIComponent(wallet.address);
    const r = await fetch(url);
    if (!r.ok) return [];
    return decodeRegistry(await r.json(), secret, origin);
  } catch {
    return [];
  }
}

// mergeMachines unions local and discovered machines by machine_id; a machine the
// user already stored locally wins, discovered-only machines are appended.
export function mergeMachines(local, discovered) {
  const seen = new Set(local.map((m) => m.machine_id));
  return local.concat(discovered.filter((m) => !seen.has(m.machine_id)));
}

// freshDevices returns the discovered machines whose machine_id is not in seenIds
// (for a one-time "new device joined" notice). Pure — the caller owns the seen set.
export function freshDevices(seenIds, discovered) {
  const seen = new Set(seenIds);
  return discovered.filter((m) => m.machine_id && !seen.has(m.machine_id));
}
