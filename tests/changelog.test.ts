import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import {
  CHANGELOG_RELEASES,
  changelogReleaseForVersion,
  latestChangelogRelease,
  mergeChangelogReleases,
  normalizeChangelogVersion,
  normalizeChangelogReleases,
  shouldShowChangelog,
} from '../src/changelog';

describe('versioned changelog', () => {
  it('keeps both changelog columns scrollable inside the viewport', () => {
    const css = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');
    expect(css).toMatch(/\.changelog-layout\s*\{[^}]*flex:\s*1\s+1\s+auto[^}]*overflow:\s*hidden/s);
    expect(css).toMatch(/\.changelog-version-list\s*\{[^}]*overflow-y:\s*auto/s);
    expect(css).toMatch(/\.changelog-content\s*\{[^}]*min-height:\s*0[^}]*overflow:\s*auto/s);
  });

  it('bundles only the v0.4.13 package-size and streamlined-release update', () => {
    expect(CHANGELOG_RELEASES).toHaveLength(1);
    expect(latestChangelogRelease()).toBe(CHANGELOG_RELEASES[0]);
    expect(normalizeChangelogVersion(' v0.4.13 ')).toBe('0.4.13');
    expect(changelogReleaseForVersion('v0.4.13')).toEqual({
      version: '0.4.13',
      date: '2026-09-03',
      title: '缩小安装包并简化安全发布',
      summary: '完整保留 v0.4.12 的礼物兼容、安全更新与内嵌 FFmpeg 功能；修正发布构建边界，不再把 FFmpeg 源码、测试工具和发布暂存目录作为 UI 资源嵌入 EXE，同时签名校验的 FFmpeg 9.0 运行时仍随主程序提供。同一法律发布者后续版本不再要求每个版本单独轮换策略 epoch。',
      highlights: [
        { label: '体积', title: '移除重复发布暂存内容', description: 'FFmpeg 源码、测试工具和发布暂存目录不再作为 UI 资源嵌入主程序。' },
        { label: '更新', title: '同一发布者一次触发发布', description: '同一法律发布者的稳定版构建、签名和发布由一次操作串联完成。' },
        { label: '安全', title: '未知法律主体继续暂停', description: '签名证书主体发生变化时仍会停止发布，等待明确审阅和授权。' },
      ],
      visuals: [],
    });
  });

  it('binds package metadata and reviewed history to v0.4.13', () => {
    const packageJSON = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8')) as { version: string };
    const packageLock = JSON.parse(readFileSync(new URL('../package-lock.json', import.meta.url), 'utf8')) as { version: string; packages: Record<string, { version?: string }> };
    const history = JSON.parse(readFileSync(new URL('../.github/changelog-history.json', import.meta.url), 'utf8')) as { releases: Array<{ version: string }> };
    expect(packageJSON.version).toBe('0.4.13');
    expect(packageLock.version).toBe('0.4.13');
    expect(packageLock.packages['']?.version).toBe('0.4.13');
    expect(history.releases.slice(0, 4).map((release) => release.version)).toEqual(['0.4.12', '0.4.10', '0.4.9', '0.4.7']);
    expect(history.releases.map((release) => release.version)).not.toContain('0.4.8');
  });

  it('opens once for a known installed version and ignores development builds', () => {
    expect(shouldShowChangelog('0.4.13', '')).toBe(true);
    expect(shouldShowChangelog('v0.4.13', '0.4.13')).toBe(false);
    expect(shouldShowChangelog('dev', '')).toBe(false);
    expect(shouldShowChangelog('9.9.9', '')).toBe(false);
  });

  it('merges hosted history over the bundled current-release fallback', () => {
    const hosted = normalizeChangelogReleases({
      releases: [
        {
          version: '0.1.0', date: '2026-08-01', title: '首个版本', summary: '首次发布。',
          highlights: [{ label: '基础', title: '可用版本', description: '完成基础能力。' }],
          visuals: [],
        },
      ],
    });
    const merged = mergeChangelogReleases(hosted);
    expect(merged.map((release) => release.version)).toEqual(['0.4.13', '0.1.0']);
    expect(normalizeChangelogReleases({ releases: [{ version: 'broken' }] })).toEqual([]);
  });

  it('ignores the removed training visual from bundled and hosted changelogs', () => {
    expect(changelogReleaseForVersion('0.4.13')?.visuals).toEqual([]);
    const [hosted] = normalizeChangelogReleases({
      releases: [{
        version: '0.2.5', date: '2026-08-09', title: '测试版本', summary: '测试在线日志。',
        highlights: [], visuals: ['training', 'broadcast'],
      }],
    });
    expect(hosted.visuals).toEqual(['broadcast']);
  });
});
