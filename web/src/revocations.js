// Owner-signed, permanent machine tombstones. The relay is an availability
// channel only: every browser verifies signatures before enforcing a record.
import { signAuth, verifyAuth } from './identity/auth.js';

const DOMAIN = 'miranda/revocation/v1';
const KEY = 'mir_revocations_v1';
const enc = new TextEncoder();
const MACHINE_ID = /^[0-9A-Za-z._-]{1,128}$/;

function bytesToB64(bytes) {
  let binary = '';
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary);
}

function b64ToBytes(value) {
  const binary = atob(value);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

// Byte-identical to identity.RevocationChallenge in Go.
export function revocationChallenge(machineID, ts) {
  return enc.encode(`${DOMAIN}\n${machineID}\n${ts}`);
}

export function signRevocation(signer, machineID, ts = Math.floor(Date.now() / 1000)) {
  if (!MACHINE_ID.test(machineID) || !Number.isSafeInteger(ts) || ts <= 0) {
    throw new Error('invalid revocation fields');
  }
  return {
    v: 1,
    owner_id: signer.address,
    machine_id: machineID,
    ts,
    signature: bytesToB64(signAuth(signer, revocationChallenge(machineID, ts))),
  };
}

export function verifyRevocation(record) {
  if (!record || record.v !== 1 || !MACHINE_ID.test(record.machine_id || '') ||
      !Number.isSafeInteger(record.ts) || record.ts <= 0 || typeof record.owner_id !== 'string') return false;
  try {
    return verifyAuth(record.owner_id, revocationChallenge(record.machine_id, record.ts), b64ToBytes(record.signature));
  } catch {
    return false;
  }
}

// Local corruption is a security error, not an empty deny-list. Callers should
// stop before rendering an attachable machine list if this throws.
export function loadRevocations(ownerID) {
  let records;
  try { records = JSON.parse(localStorage.getItem(KEY) || '[]'); }
  catch { throw new Error('local revocation store is corrupt'); }
  if (!Array.isArray(records) || records.some((r) => !verifyRevocation(r))) {
    throw new Error('local revocation store failed signature verification');
  }
  return records.filter((r) => r.owner_id === ownerID);
}

export function recordRevocation(record) {
  if (!verifyRevocation(record)) throw new Error('invalid revocation signature');
  let records;
  try { records = JSON.parse(localStorage.getItem(KEY) || '[]'); }
  catch { throw new Error('local revocation store is corrupt'); }
  if (!Array.isArray(records) || records.some((r) => !verifyRevocation(r))) {
    throw new Error('local revocation store failed signature verification');
  }
  records = records.filter((r) => !(r.owner_id === record.owner_id && r.machine_id === record.machine_id));
  records.push(record);
  records.sort((a, b) => (a.owner_id + '\0' + a.machine_id).localeCompare(b.owner_id + '\0' + b.machine_id));
  localStorage.setItem(KEY, JSON.stringify(records));
  return record;
}

export function filterRevoked(machines, records) {
  const blocked = new Set(records.map((r) => r.machine_id));
  return machines.filter((m) => !blocked.has(m.machine_id));
}

export function isMachineRevoked(machineID, ownerID) {
  return loadRevocations(ownerID).some((r) => r.machine_id === machineID);
}

export async function fetchRevocations(origin, ownerID) {
  const url = origin.replace(/\/$/, '') + '/revocations?owner_id=' + encodeURIComponent(ownerID);
  const response = await fetch(url);
  if (!response.ok) throw new Error('revocation relay returned ' + response.status);
  const records = await response.json();
  if (!Array.isArray(records) || records.some((r) => r.owner_id !== ownerID || !verifyRevocation(r))) {
    throw new Error('revocation relay returned an invalid signed record');
  }
  return records;
}

export async function syncRevocations(origin, ownerID) {
  const records = await fetchRevocations(origin, ownerID);
  for (const record of records) recordRevocation(record);
  return loadRevocations(ownerID);
}

// Persist locally before POSTing, so an offline or malicious relay cannot make
// this browser trust the target again.
export async function revokeMachine(origins, signer, machineID) {
  const record = signRevocation(signer, machineID);
  recordRevocation(record);
  const unique = [...new Set((Array.isArray(origins) ? origins : [origins]).filter(Boolean).map((x) => x.replace(/\/$/, '')))];
  const failures = [];
  for (const origin of unique) {
    try {
      const response = await fetch(origin + '/revocations', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(record),
      });
      if (!response.ok) failures.push(origin + ' (' + response.status + ')');
    } catch {
      failures.push(origin + ' (unreachable)');
    }
  }
  if (failures.length) throw new Error('saved locally, but relay publication failed: ' + failures.join(', '));
  return record;
}
