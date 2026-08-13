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

  it('bundles only the v0.4.2 release with its configuration page fix', () => {
    expect(CHANGELOG_RELEASES).toHaveLength(1);
    expect(latestChangelogRelease()).toBe(CHANGELOG_RELEASES[0]);
    expect(normalizeChangelogVersion(' v0.4.2 ')).toBe('0.4.2');
    expect(changelogReleaseForVersion('v0.4.2')).toEqual({
      version: '0.4.2',
      date: '2026-08-13',
      title: '配置页面恢复正常显示',
      summary: '修复 v0.4.1 中启动后配置页面空白的问题。',
      highlights: [
        {
          label: '配置页面',
          title: '页面内容正常加载',
          description: '补全 EXE 内缺失的启动模块，配置页面现在可以正常打开和使用。',
        },
      ],
      visuals: [],
    });
  });

  it('opens once for a known installed version and ignores development builds', () => {
    expect(shouldShowChangelog('0.4.2', '')).toBe(true);
    expect(shouldShowChangelog('v0.4.2', '0.4.2')).toBe(false);
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
    expect(merged.map((release) => release.version)).toEqual(['0.4.2', '0.1.0']);
    expect(normalizeChangelogReleases({ releases: [{ version: 'broken' }] })).toEqual([]);
  });

  it('ignores the removed training visual from bundled and hosted changelogs', () => {
    expect(changelogReleaseForVersion('0.4.2')?.visuals).toEqual([]);
    const [hosted] = normalizeChangelogReleases({
      releases: [{
        version: '0.2.5', date: '2026-08-09', title: '测试版本', summary: '测试在线日志。',
        highlights: [], visuals: ['training', 'broadcast'],
      }],
    });
    expect(hosted.visuals).toEqual(['broadcast']);
  });
});
