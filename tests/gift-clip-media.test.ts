import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { GiftReceipt } from '../src/types';
import {
  giftEffectDurationMs,
  giftEffectVisualSize,
  giftGifFrameIndex,
  loadGiftClipMediaSession,
  normalizeGiftClipDuration,
  normalizeGiftEffectLayout,
} from '../src/ui/config/gift-clip-media';

class FakeMediaHost {
  readonly children: FakeImage[] = [];

  append(...nodes: FakeImage[]): void {
    for (const node of nodes) {
      node.parentHost = this;
      this.children.push(node);
    }
  }

  remove(node: FakeImage): void {
    const index = this.children.indexOf(node);
    if (index >= 0) this.children.splice(index, 1);
    node.parentHost = null;
  }
}

class FakeImage {
  static readonly instances: FakeImage[] = [];

  className = '';
  decoding = '';
  naturalWidth = 320;
  naturalHeight = 180;
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  parentHost: FakeMediaHost | null = null;
  sourceAssignments = 0;
  private source = '';

  constructor() {
    FakeImage.instances.push(this);
  }

  set src(value: string) {
    this.source = value;
    this.sourceAssignments += 1;
    if (value.includes('kind=avatar')) {
      queueMicrotask(() => this.onerror?.());
    } else if (value.startsWith('blob:') && this.sourceAssignments === 1) {
      queueMicrotask(() => this.onload?.());
    }
  }

  get src(): string {
    return this.source;
  }

  removeAttribute(name: string): void {
    if (name === 'src') this.source = '';
  }

  remove(): void {
    this.parentHost?.remove(this);
  }
}

function receipt(animation: GiftReceipt['animation'] = { webp: 'animation.webp', durationMs: 2400 }): GiftReceipt {
  return {
    id: 'receipt-1',
    time: 1,
    giftId: 2,
    giftName: '测试礼物',
    num: 1,
    price: 100,
    totalCoin: 100,
    coinType: 'gold',
    uname: '测试观众',
    animation,
    effects: [],
  };
}

function animatedImage(): FakeImage {
  const image = FakeImage.instances.find((candidate) => candidate.className === 'gift-clip-source-animation');
  if (!image) throw new Error('animated image was not created');
  return image;
}

async function settlement(promise: Promise<void>): Promise<'resolved' | 'rejected' | 'pending'> {
  return Promise.race([
    promise.then(() => 'resolved' as const, () => 'rejected' as const),
    new Promise<'pending'>((resolve) => setTimeout(() => resolve('pending'), 0)),
  ]);
}

describe('gift clip media', () => {
  const layout = normalizeGiftEffectLayout({
    videoWidth: 1088,
    videoHeight: 1280,
    rgbFrame: [0, 0, 720, 1280],
    alphaFrame: [724, 0, 360, 640],
    fps: 30,
    frames: 390,
  });
  let revokedURLs: string[];

  beforeEach(() => {
    FakeImage.instances.length = 0;
    revokedURLs = [];
    vi.stubGlobal('Image', FakeImage as unknown as typeof Image);
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:gift-animation');
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation((url) => revokedURLs.push(url));
    vi.stubGlobal('fetch', vi.fn(async () => new Response(new Blob(['RIFF-not-a-gif']))));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('uses the RGB composite dimensions without a 480px pre-scale', () => {
    expect(giftEffectVisualSize(layout)).toEqual({ width: 720, height: 1280 });
    expect(giftEffectDurationMs(layout)).toBe(13_000);
  });

  it.each([
    {
      videoWidth: 5000,
      videoHeight: 10,
      rgbFrame: [0, 0, 4097, 10],
      alphaFrame: [4097, 0, 1, 10],
    },
    {
      videoWidth: 10,
      videoHeight: 5000,
      rgbFrame: [0, 0, 10, 4097],
      alphaFrame: [0, 4097, 10, 1],
    },
  ])('rejects a full-effect composite dimension above 4096px before rendering', (oversizedLayout) => {
    expect(() => normalizeGiftEffectLayout({ ...oversizedLayout, fps: 30, frames: 60 }))
      .toThrow('礼物特效坐标超出允许范围');
  });

  it('selects deterministic GIF frames across loops', () => {
    expect([0, 219, 220, 500, 660].map((time) => giftGifFrameIndex([220, 220, 220], time)))
      .toEqual([0, 0, 1, 2, 0]);
  });

  it('clamps missing and abnormal durations', () => {
    expect([undefined, 200, 2200, 60_000].map(normalizeGiftClipDuration)).toEqual([3000, 1000, 2200, 15_000]);
  });

  it('rejects packed-alpha coordinates outside the video', () => {
    expect(() => normalizeGiftEffectLayout({ ...layout, rgbFrame: [0, 0, 1200, 1280] }))
      .toThrow('礼物特效坐标无效');
  });

  it('loads an animated-image session and exposes its observable visual state', async () => {
    const host = new FakeMediaHost();
    const session = await loadGiftClipMediaSession(receipt(), host as unknown as HTMLElement);

    expect({
      width: session.width,
      height: session.height,
      durationMs: session.durationMs,
      sourceLabel: session.sourceLabel,
      avatar: session.avatar,
    }).toEqual({ width: 320, height: 180, durationMs: 2400, sourceLabel: '短动画', avatar: null });
    expect(session.visualAt(0)?.source).toBe(host.children[0]);
  });

  it('coalesces concurrent animated-image restarts into one reload', async () => {
    const session = await loadGiftClipMediaSession(receipt(), new FakeMediaHost() as unknown as HTMLElement);
    const image = animatedImage();
    const assignmentsBeforeRestart = image.sourceAssignments;

    const first = session.restart();
    const second = session.restart();

    expect(second).toBe(first);
    expect(image.sourceAssignments).toBe(assignmentsBeforeRestart + 1);
    image.onload?.();
    await expect(first).resolves.toBeUndefined();
  });

  it('rejects an in-flight animated-image restart when disposed', async () => {
    const session = await loadGiftClipMediaSession(receipt(), new FakeMediaHost() as unknown as HTMLElement);
    const restart = session.restart();

    session.dispose();

    await expect(settlement(restart)).resolves.toBe('rejected');
    await expect(restart).rejects.toThrow('礼物动画会话已释放');
  });

  it('removes its node and revokes its object URL exactly once across repeated disposal', async () => {
    const host = new FakeMediaHost();
    const session = await loadGiftClipMediaSession(receipt(), host as unknown as HTMLElement);

    session.dispose();
    session.dispose();

    expect(host.children).toEqual([]);
    expect(revokedURLs).toEqual(['blob:gift-animation']);
    expect(session.visualAt(0)).toBeNull();
  });

  it('labels short-animation fallback after the complete effect fails', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
      const url = String(input);
      if (url.includes('kind=effect-')) return new Response(null, { status: 500 });
      return new Response(new Blob(['RIFF-not-a-gif']));
    }));
    const host = new FakeMediaHost();

    const session = await loadGiftClipMediaSession(receipt({
      mp4: 'effect.mp4',
      mp4Json: 'effect.json',
      webp: 'animation.webp',
      durationMs: 2400,
    }), host as unknown as HTMLElement);

    expect(session.sourceLabel).toBe('短动画回退');
    expect(session.visualAt(0)?.source).toBe(host.children[0]);
  });
});
