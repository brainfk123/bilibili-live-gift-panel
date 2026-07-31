import { describe, it, expect } from 'vitest';
import { deflateSync } from 'node:zlib';
import { encodePacket, encodeJson, decodePackets, decodeText, inflate, OP_AUTH, OP_MESSAGE } from '../src/bilibili/protocol';

describe('protocol encode/decode', () => {
  it('round-trips a simple packet', () => {
    const body = new TextEncoder().encode('hello');
    const data = encodePacket(OP_AUTH, body);
    const packets = decodePackets(data);
    expect(packets).toHaveLength(1);
    expect(packets[0].op).toBe(OP_AUTH);
    expect(decodeText(packets[0].body)).toBe('hello');
  });

  it('decodes multiple concatenated packets', () => {
    const a = encodePacket(1, new TextEncoder().encode('a'));
    const b = encodePacket(2, new TextEncoder().encode('b'));
    const merged = new Uint8Array(a.length + b.length);
    merged.set(a);
    merged.set(b, a.length);
    const packets = decodePackets(merged);
    expect(packets).toHaveLength(2);
    expect(decodeText(packets[0].body)).toBe('a');
    expect(decodeText(packets[1].body)).toBe('b');
  });

  it('encodes JSON body', () => {
    const data = encodeJson(OP_MESSAGE, { cmd: 'TEST', data: 1 });
    const packets = decodePackets(data);
    expect(JSON.parse(decodeText(packets[0].body))).toEqual({ cmd: 'TEST', data: 1 });
  });

  it('inflates zlib data', async () => {
    const raw = new TextEncoder().encode(JSON.stringify({ hello: 'world' }));
    const compressed = new Uint8Array(deflateSync(raw));
    const out = await inflate(compressed);
    expect(decodeText(out)).toBe(JSON.stringify({ hello: 'world' }));
  });
});
