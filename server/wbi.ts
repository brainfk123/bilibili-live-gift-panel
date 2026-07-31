import { createHash } from 'node:crypto';

export const WBI_KEY_INDEX_TABLE = [
  46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35,
  27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
];

export function extractWbiKey(imgUrl: string, subUrl: string): string {
  const imgKey = imgUrl.split('/').pop()!.split('.')[0];
  const subKey = subUrl.split('/').pop()!.split('.')[0];
  const shuffled = imgKey + subKey;
  let key = '';
  for (const i of WBI_KEY_INDEX_TABLE) {
    if (i < shuffled.length) key += shuffled[i];
  }
  return key;
}

export function addWbiSign(
  params: Record<string, string | number>,
  wbiKey: string,
): Record<string, string> {
  const wts = String(Math.floor(Date.now() / 1000));
  const toSign: Record<string, string> = {};
  for (const [k, v] of Object.entries({ ...params, wts })) {
    toSign[k] = String(v).replace(/[!'()*]/g, '');
  }
  const query = Object.keys(toSign)
    .sort()
    .map((k) => `${k}=${encodeURIComponent(toSign[k])}`)
    .join('&');
  const wRid = createHash('md5').update(query + wbiKey).digest('hex');
  return { ...params, wts, w_rid: wRid } as Record<string, string>;
}
