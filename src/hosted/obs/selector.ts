export const maxOBSSelectorEncodedLength = 64 * 1024;
export const maxOBSLinkURLLength = maxOBSSelectorEncodedLength + 1024;

export type OBSOutputSelector = { kind: 'attribute' | 'scene' | 'gift-target'; id: string; attributeIds: readonly string[] };

export function encodeOBSOutputSelector(selector: OBSOutputSelector): string | undefined {
  if (!validSelector(selector)) return undefined;
  const wire = selector.kind === 'scene'
    ? { kind: selector.kind, id: selector.id, attributes: [...selector.attributeIds] }
    : { kind: selector.kind, id: selector.id };
  const bytes = new TextEncoder().encode(JSON.stringify(wire));
  let binary = '';
  for (let offset = 0; offset < bytes.length; offset += 0x8000) binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  const encoded = btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
  return encoded.length > 0 && encoded.length <= maxOBSSelectorEncodedLength ? encoded : undefined;
}

export function parseOBSOutputSelector(value: string | null): OBSOutputSelector | undefined {
  if (!value || value.length > maxOBSSelectorEncodedLength || !/^[A-Za-z0-9_-]+$/.test(value)) return undefined;
  try {
    const padding = '='.repeat((4 - value.length % 4) % 4);
    const binary = atob(value.replaceAll('-', '+').replaceAll('_', '/') + padding);
    const bytes = new Uint8Array(binary.length);
    for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
    const decoded: unknown = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes));
    if (!decoded || typeof decoded !== 'object' || Array.isArray(decoded)) return undefined;
    const item = decoded as Record<string, unknown>;
    let selector: OBSOutputSelector;
    if ((item.kind === 'attribute' || item.kind === 'gift-target') && exactKeys(item, ['kind', 'id']) && typeof item.id === 'string') {
      selector = { kind: item.kind, id: item.id, attributeIds: item.kind === 'attribute' ? [item.id] : [] };
    } else if (item.kind === 'scene' && exactKeys(item, ['kind', 'id', 'attributes']) && typeof item.id === 'string' && Array.isArray(item.attributes) && item.attributes.every((id) => typeof id === 'string')) {
      selector = { kind: 'scene', id: item.id, attributeIds: item.attributes as string[] };
    } else return undefined;
    return encodeOBSOutputSelector(selector) === value ? selector : undefined;
  } catch { return undefined; }
}

function validSelector(selector: OBSOutputSelector): boolean {
  if (!selector.id) return false;
  if (selector.kind === 'attribute') return selector.attributeIds.length === 1 && selector.attributeIds[0] === selector.id;
  if (selector.kind === 'gift-target') return selector.attributeIds.length === 0;
  return selector.kind === 'scene' && selector.attributeIds.length > 0 && selector.attributeIds.every(Boolean) && new Set(selector.attributeIds).size === selector.attributeIds.length;
}

function exactKeys(value: Record<string, unknown>, expected: string[]): boolean {
  const keys = Object.keys(value);
  return keys.length === expected.length && expected.every((key) => Object.hasOwn(value, key));
}
