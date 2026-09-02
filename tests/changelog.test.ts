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

  it('bundles only the v0.4.12 release with gift compatibility and update trust', () => {
    expect(CHANGELOG_RELEASES).toHaveLength(1);
    expect(latestChangelogRelease()).toBe(CHANGELOG_RELEASES[0]);
    expect(normalizeChangelogVersion(' v0.4.12 ')).toBe('0.4.12');
    expect(changelogReleaseForVersion('v0.4.12')).toEqual({
      version: '0.4.12',
      date: '2026-09-02',
      title: '修复新版礼物消息并升级安全更新',
      summary: '修复 B 站新版 SEND_GIFT_V2 普通礼物消息未触发属性玩法的问题；自动更新现在通过签名发布者策略、版本通道路由和回滚保护校验下载内容。主程序继续内嵌固定 FFmpeg 9.0，发布包同时提供经过签名和校验的独立 FFmpeg，无需运行时联网下载。',
      highlights: [
        { label: '礼物', title: '新版普通礼物恢复响应', description: '兼容 B 站 SEND_GIFT_V2 消息，已配置礼物可以正常触发属性规则。' },
        { label: '更新', title: '发布者信任与防回滚', description: '更新前会校验签名策略、目标版本、文件哈希和发布者身份。' },
        { label: 'FFmpeg', title: '固定版本随发布包提供', description: '继续使用经过签名校验的 FFmpeg 9.0，避免国内镜像下载失败。' },
      ],
      visuals: [],
    });
  });

  it('opens once for a known installed version and ignores development builds', () => {
    expect(shouldShowChangelog('0.4.12', '')).toBe(true);
    expect(shouldShowChangelog('v0.4.12', '0.4.12')).toBe(false);
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
    expect(merged.map((release) => release.version)).toEqual(['0.4.12', '0.1.0']);
    expect(normalizeChangelogReleases({ releases: [{ version: 'broken' }] })).toEqual([]);
  });

  it('ignores the removed training visual from bundled and hosted changelogs', () => {
    expect(changelogReleaseForVersion('0.4.12')?.visuals).toEqual([]);
    const [hosted] = normalizeChangelogReleases({
      releases: [{
        version: '0.2.5', date: '2026-08-09', title: '测试版本', summary: '测试在线日志。',
        highlights: [], visuals: ['training', 'broadcast'],
      }],
    });
    expect(hosted.visuals).toEqual(['broadcast']);
  });
});
