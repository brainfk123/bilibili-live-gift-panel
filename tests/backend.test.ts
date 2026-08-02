import { afterEach, describe, expect, it, vi } from 'vitest';
import { startPagePresence } from '../src/backend';

describe('page presence', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('opens one presence stream and reconnects after a restored page', () => {
    const opened: Array<{ url: string; close: ReturnType<typeof vi.fn> }> = [];
    const listeners = new Map<string, () => void>();
    class FakeEventSource {
      close = vi.fn();

      constructor(readonly url: string) {
        opened.push({ url, close: this.close });
      }
    }
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.stubGlobal('crypto', { randomUUID: () => 'page-session-1' });
    vi.stubGlobal('addEventListener', (name: string, listener: () => void) => listeners.set(name, listener));
    vi.stubGlobal('removeEventListener', vi.fn());

    const stop = startPagePresence('config');
    expect(opened).toHaveLength(1);
    expect(opened[0].url).toBe('/api/pages/presence/stream?mode=config&id=page-session-1');

    listeners.get('pagehide')?.();
    expect(opened[0].close).toHaveBeenCalledOnce();
    listeners.get('pageshow')?.();
    expect(opened).toHaveLength(2);

    stop();
    expect(opened[1].close).toHaveBeenCalledOnce();
  });
});
