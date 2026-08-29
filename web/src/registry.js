// web/src/registry.js — discover your machines from the relay's encrypted device
// registry (B2). Mirrors go/internal/client/registry.go. The relay serves opaque
// blobs keyed by owner id; only an owner-root holder (registryKey) can open them, so a
// forged/garbage blob fails to open and is silently dropped. Discovery only — the
// Noise data plane and attach path are unchanged.
import { registryKey, openRecord, sealRecord } from './identity/registry.js';

const td = new TextDecoder();

function b64ToBytes(s) {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function bytesToB64(bytes) {
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}

// sealMachineRecord seals a discovery record for one machine under the owner
// root (same shape pairing provisions): the returned ts is the name's
// last-writer-wins timestamp (see mergeMachines). Used by machine rename — the
// agent cannot seal records itself, so the renaming client re-seals and
// delivers the blob over the authenticated session.
export function sealMachineRecord(secret, m) {
  const ts = Math.floor(Date.now() / 1000);
  const record = new TextEncoder().encode(JSON.stringify({
    v: 1,
    name: m.name,
    host_pub: m.host_pub,
    signal_url: m.signal_url || '',
    ts,
  }));
  const blob = sealRecord(registryKey(secret), crypto.getRandomValues(new Uint8Array(12)), record, m.machine_id);
  return { blob: bytesToB64(blob), ts };
}

// decodeRegistry turns the relay's `[{machine_id, blob}]` into machines, dropping
// any blob that fails to open (a forgery, or one sealed under a different owner root).
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
      name_ts: rec.ts || 0,
    });
  }
  return out;
}

// fetchMachines GETs the owner's registry from `origin` and decodes it. Best-effort:
// any failure (relay down, not served same-origin, bad JSON) returns [] so the caller
// falls back to the locally-stored machine list without surfacing noise.
export async function fetchMachines(origin, signer, secret) {
  try {
    const url = origin.replace(/\/$/, '') + '/registry?owner_id=' + encodeURIComponent(signer.address);
    const r = await fetch(url);
    if (!r.ok) return [];
    return decodeRegistry(await r.json(), secret, origin);
  } catch {
    return [];
  }
}

// mergeMachines unions local and discovered machines by machine_id. A verified
// registry record is canonical for host_pub and signal; local-only rows stay.
// The display name is last-writer-wins on name_ts (mirrors Go MergeMachines): a
// registry record sealed after the local name was set carries a rename made on
// another device, so it wins; a local rename not yet delivered to the machine
// keeps winning until the machine republishes.
export function mergeMachines(local, discovered) {
  const byId = new Map();
  for (const m of local || []) {
    if (m && m.machine_id) byId.set(m.machine_id, { ...m });
  }
  for (const m of discovered || []) {
    if (!m || !m.machine_id) continue;
    const prev = byId.get(m.machine_id);
    if (!prev) {
      byId.set(m.machine_id, { ...m });
      continue;
    }
    const registryNameWins = m.name && (!prev.name || (m.name_ts || 0) >= (prev.name_ts || 0));
    byId.set(m.machine_id, {
      ...prev,
      ...m,
      name: registryNameWins ? m.name : prev.name,
      name_ts: registryNameWins ? (m.name_ts || 0) : (prev.name_ts || 0),
      host_pub: m.host_pub || prev.host_pub,
      signal: m.signal || prev.signal,
    });
  }
  return [...byId.values()];
}

// freshDevices returns the discovered machines whose machine_id is not in seenIds
// (for a one-time "new device joined" notice). Pure — the caller owns the seen set.
export function freshDevices(seenIds, discovered) {
  const seen = new Set(seenIds);
  return discovered.filter((m) => m.machine_id && !seen.has(m.machine_id));
}
