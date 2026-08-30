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

  it('bundles only the v0.4.10 release with safe gift diagnostics', () => {
    expect(CHANGELOG_RELEASES).toHaveLength(1);
    expect(latestChangelogRelease()).toBe(CHANGELOG_RELEASES[0]);
    expect(normalizeChangelogVersion(' v0.4.10 ')).toBe('0.4.10');
    expect(changelogReleaseForVersion('v0.4.10')).toEqual({
      version: '0.4.10',
      date: '2026-08-30',
      title: '增强礼物诊断与加班机摘要',
      summary: '普通礼物解析失败时，运行日志会记录脱敏后的命令、失败阶段和字段类型，便于继续定位 B 站消息变化；同时修正加班机调整时长后规则卡片仍显示旧分钟数的问题。主程序仍内嵌固定 FFmpeg 9.0，发布页另提供经过签名和校验的 ffmpeg-windows-x64.exe 及 SHA-256 作为可选下载，主程序用户无需另行下载。日志不会记录 UID、昵称、消息内容或原始礼物数据。',
      highlights: [],
      visuals: [],
    });
  });

  it('opens once for a known installed version and ignores development builds', () => {
    expect(shouldShowChangelog('0.4.10', '')).toBe(true);
    expect(shouldShowChangelog('v0.4.10', '0.4.10')).toBe(false);
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
    expect(merged.map((release) => release.version)).toEqual(['0.4.10', '0.1.0']);
    expect(normalizeChangelogReleases({ releases: [{ version: 'broken' }] })).toEqual([]);
  });

  it('ignores the removed training visual from bundled and hosted changelogs', () => {
    expect(changelogReleaseForVersion('0.4.10')?.visuals).toEqual([]);
    const [hosted] = normalizeChangelogReleases({
      releases: [{
        version: '0.2.5', date: '2026-08-09', title: '测试版本', summary: '测试在线日志。',
        highlights: [], visuals: ['training', 'broadcast'],
      }],
    });
    expect(hosted.visuals).toEqual(['broadcast']);
  });
});
