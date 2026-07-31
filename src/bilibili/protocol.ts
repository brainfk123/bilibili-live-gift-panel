export const OP_AUTH = 7;
export const OP_AUTH_REPLY = 8;
export const OP_HEARTBEAT = 2;
export const OP_MESSAGE = 5;

export interface Packet {
  protover: number;
  op: number;
  body: Uint8Array;
}

const HEADER_LEN = 16;

export function encodePacket(op: number, body: Uint8Array, protover = 0): Uint8Array {
  const totalLen = HEADER_LEN + body.length;
  const buf = new ArrayBuffer(totalLen);
  const view = new DataView(buf);
  view.setUint32(0, totalLen);
  view.setUint16(4, HEADER_LEN);
  view.setUint16(6, protover);
  view.setUint32(8, op);
  view.setUint32(12, 1);
  new Uint8Array(buf, HEADER_LEN).set(body);
  return new Uint8Array(buf);
}

export function encodeJson(op: number, obj: unknown, protover = 0): Uint8Array {
  return encodePacket(op, new TextEncoder().encode(JSON.stringify(obj)), protover);
}

export function decodePackets(data: Uint8Array): Packet[] {
  const packets: Packet[] = [];
  let offset = 0;
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  while (offset + HEADER_LEN <= data.length) {
    const totalLen = view.getUint32(offset);
    const headerLen = view.getUint16(offset + 4);
    const protover = view.getUint16(offset + 6);
    const op = view.getUint32(offset + 8);
    if (totalLen < headerLen || offset + totalLen > data.length) break;
    const body = data.slice(offset + headerLen, offset + totalLen);
    packets.push({ protover, op, body });
    offset += totalLen;
  }
  return packets;
}

export function decodeText(body: Uint8Array): string {
  return new TextDecoder().decode(body);
}

export async function inflate(body: Uint8Array): Promise<Uint8Array> {
  const ds = new DecompressionStream('deflate');
  const copy = new Uint8Array(body);
  const stream = new Blob([copy]).stream().pipeThrough(ds);
  const buf = await new Response(stream).arrayBuffer();
  return new Uint8Array(buf);
}
