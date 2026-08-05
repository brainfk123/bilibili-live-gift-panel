import {
  CHANGELOG_RELEASES,
  changelogReleaseForVersion,
  latestChangelogRelease,
  type ChangelogRelease,
  type ChangelogVisual,
} from '../../changelog';
import { el } from '../common';

export interface ChangelogDialogOptions {
  currentVersion?: string;
  releases?: ChangelogRelease[];
  onClose: (seenVersion: string) => void;
}

export function createChangelogDialog(options: ChangelogDialogOptions): HTMLElement {
  const releases = options.releases?.length ? options.releases : CHANGELOG_RELEASES;
  const overlay = el('div', { class: 'overlay changelog-overlay' });
  const dialog = el('section', {
    class: 'card changelog-dialog',
    role: 'dialog',
    ariaLabel: '更新日志',
  } as any);
  const content = el('div', { class: 'changelog-content' });
  const versionList = el('nav', { class: 'changelog-version-list', ariaLabel: '更新日志版本' } as any);
  let activeRelease = changelogReleaseForVersion(options.currentVersion, releases) ?? latestChangelogRelease(releases);
  const seenVersion = activeRelease.version;

  const close = (): void => {
    overlay.remove();
    options.onClose(seenVersion);
  };
  const closeButton = el('button', {
    class: 'modal-close changelog-close', type: 'button', text: '×', ariaLabel: '关闭更新日志',
  } as any) as HTMLButtonElement;
  closeButton.onclick = close;

  const renderVersions = (): void => {
    versionList.replaceChildren();
    for (const release of releases) {
      const selected = release.version === activeRelease.version;
      const button = el('button', {
        class: `changelog-version${selected ? ' is-active' : ''}`,
        type: 'button',
        ariaPressed: String(selected),
      } as any) as HTMLButtonElement;
      button.append(
        el('strong', { text: `v${release.version}` }),
        el('span', { text: release.date }),
      );
      button.onclick = () => {
        activeRelease = release;
        renderVersions();
        renderRelease(content, release);
      };
      versionList.append(button);
    }
  };

  dialog.append(
    el('header', { class: 'changelog-header' }, [
      el('div', {}, [
        el('span', { class: 'section-kicker', text: '版本故事' }),
        el('h2', { text: '这次更新了什么？' }),
        el('p', { text: '只看主播真正用得到的变化，几分钟了解新功能。' }),
      ]),
      closeButton,
    ]),
    el('div', { class: 'changelog-layout' }, [versionList, content]),
  );
  renderVersions();
  renderRelease(content, activeRelease);
  overlay.append(dialog);
  overlay.onclick = (event) => {
    if (event.target === overlay) close();
  };
  dialog.onkeydown = (event) => {
    if (event.key === 'Escape') close();
  };
  return overlay;
}

function renderRelease(host: HTMLElement, release: ChangelogRelease): void {
  const highlights = el('div', { class: 'changelog-highlights' });
  release.highlights.forEach((highlight, index) => {
    highlights.append(el('article', { class: 'changelog-highlight' }, [
      el('span', { class: 'changelog-highlight-index', text: String(index + 1).padStart(2, '0') }),
      el('div', {}, [
        el('span', { class: 'changelog-highlight-label', text: highlight.label }),
        el('h3', { text: highlight.title }),
        el('p', { text: highlight.description }),
      ]),
    ]));
  });

  const visuals = el('div', { class: 'changelog-visual-grid' });
  for (const visual of release.visuals) visuals.append(createVisual(visual));
  host.replaceChildren(
    el('section', { class: 'changelog-release-intro' }, [
      el('div', {}, [
        el('span', { class: 'changelog-release-version', text: `v${release.version}` }),
        el('time', { text: release.date }),
      ]),
      el('h3', { text: release.title }),
      el('p', { text: release.summary }),
    ]),
    visuals,
    highlights,
  );
}

function createVisual(type: ChangelogVisual): HTMLElement {
  if (type === 'scene') {
    return el('figure', { class: 'changelog-visual is-scene' }, [
      el('figcaption', { text: '组合面板' }),
      el('div', { class: 'changelog-scene-grid' }, [
        el('div', {}, [el('span', { text: '继续挑战' }), el('strong', { text: '18 票' })]),
        el('div', {}, [el('span', { text: '休息一下' }), el('strong', { text: '12 票' })]),
      ]),
      el('div', { class: 'changelog-scene-broadcast', text: '观众礼物正在依次播报…' }),
    ]);
  }
  if (type === 'broadcast') {
    return el('figure', { class: 'changelog-visual is-broadcast' }, [
      el('figcaption', { text: '连续送礼不丢消息' }),
      el('div', { class: 'changelog-broadcast-track' }, [
        broadcastPreview('小电视飞船', '+60 秒', 'is-first'),
        broadcastPreview('粉丝团灯牌', '+6 秒', 'is-second'),
        broadcastPreview('人气票', '+1 票', 'is-third'),
      ]),
    ]);
  }
  return el('figure', { class: 'changelog-visual is-training' }, [
    el('figcaption', { text: '训练中心' }),
    el('div', { class: 'changelog-training-progress' }, [
      el('strong', { text: '12/12' }),
      el('span', { text: '主线任务' }),
    ]),
    el('ol', {}, [
      el('li', { class: 'is-done', text: '连接直播间' }),
      el('li', { class: 'is-done', text: '从空白创建加班机' }),
      el('li', { class: 'is-active', text: '预览并加入 OBS' }),
    ]),
  ]);
}

function broadcastPreview(gift: string, delta: string, className: string): HTMLElement {
  return el('div', { class: `changelog-broadcast-item ${className}` }, [
    el('span', { class: 'changelog-broadcast-avatar', text: '礼' }),
    el('span', { text: gift }),
    el('strong', { text: delta }),
  ]);
}
