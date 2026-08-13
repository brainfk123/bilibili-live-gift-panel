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

  it('bundles only the v0.4.1 release with its user-visible reliability notes', () => {
    expect(CHANGELOG_RELEASES).toHaveLength(1);
    expect(latestChangelogRelease()).toBe(CHANGELOG_RELEASES[0]);
    expect(normalizeChangelogVersion(' v0.4.1 ')).toBe('0.4.1');
    expect(changelogReleaseForVersion('v0.4.1')).toEqual({
      version: '0.4.1',
      date: '2026-08-13',
      title: '视频导出与礼物接收更可靠',
      summary: '礼物动画导出和本地接收处理更稳定可靠。',
      highlights: [
        {
          label: '视频导出',
          title: '成片不再跟着页面卡顿',
          description: '内嵌并校验的 FFmpeg 按固定 30fps、输入时长自适应生成视频；硬件编码失败会自动兼容软件模式。',
        },
        {
          label: 'OBS',
          title: '主数值与强调色正确显示',
          description: 'OBS 浏览器源会显示属性主数值，文字和主题会正确应用你设置的强调色。',
        },
        {
          label: '模拟',
          title: '保存不会误写直播属性',
          description: '规则或定时器模拟只推进临时草稿，保存配置不会误写直播真实属性值。',
        },
        {
          label: '礼物接收',
          title: '断线与重启后继续处理',
          description: '缩短断线重连窗口；已接收礼物会先写入本地队列，重启后继续处理，并提示可能漏礼物的断线时段和队列异常。',
        },
      ],
      visuals: [],
    });
  });

  it('opens once for a known installed version and ignores development builds', () => {
    expect(shouldShowChangelog('0.4.1', '')).toBe(true);
    expect(shouldShowChangelog('v0.4.1', '0.4.1')).toBe(false);
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
    expect(merged.map((release) => release.version)).toEqual(['0.4.1', '0.1.0']);
    expect(normalizeChangelogReleases({ releases: [{ version: 'broken' }] })).toEqual([]);
  });

  it('ignores the removed training visual from bundled and hosted changelogs', () => {
    expect(changelogReleaseForVersion('0.4.1')?.visuals).toEqual([]);
    const [hosted] = normalizeChangelogReleases({
      releases: [{
        version: '0.2.5', date: '2026-08-09', title: '测试版本', summary: '测试在线日志。',
        highlights: [], visuals: ['training', 'broadcast'],
      }],
    });
    expect(hosted.visuals).toEqual(['broadcast']);
  });
});
