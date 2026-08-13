import { describe, expect, it, vi } from 'vitest';
import {
  sanitizeGiftClipFilename,
  triggerGiftClipDownload,
} from '../src/ui/config/gift-clip-download';

describe('gift clip download', () => {
  it('sanitizes MP4 filenames without including the sender UID', () => {
    const filename = sanitizeGiftClipFilename({
      giftName: '心动/盲盒:*?',
      uname: '观众<测试>|',
      time: new Date(2024, 0, 2, 3, 4, 5).getTime(),
    });

    expect(filename).toBe('心动-盲盒----观众-测试---20240102-030405.mp4');
    expect(filename).not.toContain('UID');
  });

  it('downloads through a temporary anchor and removes it afterwards', () => {
    const anchor = { href: '', download: '', click: vi.fn(), remove: vi.fn() };
    const append = vi.fn();
    const targetDocument = {
      createElement: vi.fn(() => anchor),
      body: { append },
    } as unknown as Document;

    triggerGiftClipDownload('/api/gift-clips/job/video', '礼物回放.mp4', targetDocument);

    expect(anchor).toMatchObject({ href: '/api/gift-clips/job/video', download: '礼物回放.mp4' });
    expect(append).toHaveBeenCalledWith(anchor);
    expect(anchor.click).toHaveBeenCalledOnce();
    expect(anchor.remove).toHaveBeenCalledOnce();
  });
});
