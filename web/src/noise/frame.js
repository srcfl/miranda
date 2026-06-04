// web/src/noise/frame.js
// Mirrors go/internal/noise/frame.go byte-for-byte.
export const FRAME_DATA = 0x01;
export const FRAME_RESIZE = 0x02;
export const FRAME_HELLO = 0x03;

export function encodeData(bytes) {
  const out = new Uint8Array(1 + bytes.length);
  out[0] = FRAME_DATA;
  out.set(bytes, 1);
  return out;
}

export function encodeResize(cols, rows) {
  const out = new Uint8Array(5);
  out[0] = FRAME_RESIZE;
  const dv = new DataView(out.buffer);
  dv.setUint16(1, cols, false); // big-endian
  dv.setUint16(3, rows, false);
  return out;
}

export function encodeHello(jsonBytes) {
  const out = new Uint8Array(1 + jsonBytes.length);
  out[0] = FRAME_HELLO;
  out.set(jsonBytes, 1);
  return out;
}

export function decodeFrame(bytes) {
  if (bytes.length < 1) throw new Error('empty frame');
  return { type: bytes[0], payload: bytes.slice(1) };
}

export function decodeResize(payload) {
  if (payload.length !== 4) throw new Error('resize payload must be 4 bytes');
  const dv = new DataView(payload.buffer, payload.byteOffset, payload.byteLength);
  return { cols: dv.getUint16(0, false), rows: dv.getUint16(2, false) };
}
