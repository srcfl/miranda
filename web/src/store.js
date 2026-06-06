// web/src/store.js — the local machine list (localStorage). A synced, passkey-
// encrypted directory ("your machines on every device") is a later milestone;
// for now each browser remembers the machines it paired.
const KEY = 'tr_machines';

export function listMachines() {
  try {
    return JSON.parse(localStorage.getItem(KEY) || '[]');
  } catch {
    return [];
  }
}

export function addMachine(m) {
  const list = listMachines().filter((x) => x.machine_id !== m.machine_id);
  list.push(m);
  localStorage.setItem(KEY, JSON.stringify(list));
  return list;
}

export function removeMachine(machineId) {
  localStorage.setItem(KEY, JSON.stringify(listMachines().filter((x) => x.machine_id !== machineId)));
}
