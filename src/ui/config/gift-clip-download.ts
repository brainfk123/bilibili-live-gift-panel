import type { GiftReceipt } from '../../types';

export function sanitizeGiftClipFilename(
  receipt: Pick<GiftReceipt, 'giftName' | 'uname' | 'time'>,
): string {
  const timestamp = new Date(receipt.time < 1_000_000_000_000 ? receipt.time * 1000 : receipt.time);
  const date = Number.isNaN(timestamp.getTime())
    ? 'unknown-time'
    : [
      timestamp.getFullYear(),
      String(timestamp.getMonth() + 1).padStart(2, '0'),
      String(timestamp.getDate()).padStart(2, '0'),
      '-',
      String(timestamp.getHours()).padStart(2, '0'),
      String(timestamp.getMinutes()).padStart(2, '0'),
      String(timestamp.getSeconds()).padStart(2, '0'),
    ].join('');
  const safe = `${receipt.giftName || '礼物'}-${receipt.uname || '观众'}`
    .replace(/[\\/:*?"<>|\u0000-\u001f]/g, '-')
    .replace(/\s+/g, ' ')
    .trim()
    .slice(0, 72) || '礼物回放';
  return `${safe}-${date}.mp4`;
}

export function triggerGiftClipDownload(
  url: string,
  filename: string,
  targetDocument: Document = document,
): void {
  const link = targetDocument.createElement('a');
  link.href = url;
  link.download = filename;
  targetDocument.body.append(link);
  link.click();
  link.remove();
}
