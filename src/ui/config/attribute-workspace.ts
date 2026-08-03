import { el } from '../common';
import type { TutorialLesson } from '../../types';
import type { AttributeWorkspaceSection, TutorialLessonState } from './wizard';

export interface AttributeWorkspaceSectionDefinition {
  id: AttributeWorkspaceSection;
  label: string;
  badge?: () => string;
  content: HTMLElement;
}

interface AttributeWorkspaceOptions {
  ariaLabel: string;
  header: HTMLElement;
  footer: HTMLElement;
  sections: AttributeWorkspaceSectionDefinition[];
  lessons: TutorialLessonState[];
  initialSection: AttributeWorkspaceSection;
  trainingVisible: boolean;
  onSectionChange?: (section: AttributeWorkspaceSection) => void;
  onLessonClick?: (lesson: TutorialLesson) => void;
}

export interface AttributeWorkspace {
  element: HTMLElement;
  activeSection: () => AttributeWorkspaceSection;
  setSection: (section: AttributeWorkspaceSection) => void;
  updateLessons: (lessons: TutorialLessonState[]) => void;
  refreshBadges: () => void;
  setTrainingVisible: (visible: boolean) => void;
}

export function createAttributeWorkspace(options: AttributeWorkspaceOptions): AttributeWorkspace {
  const element = el('section', {
    class: 'card attribute-modal attribute-workbench',
    role: 'dialog',
    ariaLabel: options.ariaLabel,
  } as any);
  const rail = el('aside', { class: 'attribute-training-rail', ariaLabel: '加班机训练任务' } as any);
  const lessonList = el('ol', { class: 'attribute-training-lessons' });
  const progress = el('span', { class: 'attribute-training-progress' });
  rail.append(
    el('div', { class: 'attribute-training-title' }, [
      progress,
      el('div', {}, [
        el('strong', { text: '加班机训练任务' }),
        el('small', { text: '完成后可直接用于直播' }),
      ]),
    ]),
    lessonList,
    el('p', { class: 'attribute-training-note', text: '每一步都操作真实配置；安全模拟不会修改直播数值。' }),
  );

  const main = el('div', { class: 'attribute-workbench-main' });
  const tabs = el('nav', { class: 'attribute-workbench-tabs', ariaLabel: '属性工作区' } as any);
  const panels = el('div', { class: 'attribute-workbench-panels' });
  const tabBySection = new Map<AttributeWorkspaceSection, HTMLButtonElement>();
  const panelBySection = new Map<AttributeWorkspaceSection, HTMLElement>();
  const badgeRefreshers: Array<() => void> = [];
  let currentSection = options.initialSection;

  const setSection = (section: AttributeWorkspaceSection): void => {
    if (!panelBySection.has(section)) return;
    currentSection = section;
    for (const [candidate, button] of tabBySection) {
      const active = candidate === section;
      button.classList.toggle('is-active', active);
      button.setAttribute('aria-selected', String(active));
    }
    for (const [candidate, panel] of panelBySection) panel.hidden = candidate !== section;
    options.onSectionChange?.(section);
  };
  const setTrainingVisible = (visible: boolean): void => {
    rail.hidden = !visible;
    element.classList.toggle('is-training-hidden', !visible);
  };

  for (const section of options.sections) {
    const button = el('button', {
      class: 'attribute-workbench-tab',
      type: 'button',
      role: 'tab',
      ariaSelected: 'false',
    } as any) as HTMLButtonElement;
    const badge = el('span', { class: 'attribute-workbench-tab-badge' });
    const updateBadge = (): void => {
      const value = section.badge?.() ?? '';
      badge.textContent = value;
      badge.hidden = value === '';
    };
    badgeRefreshers.push(updateBadge);
    updateBadge();
    button.append(el('span', { text: section.label }), badge);
    button.onclick = () => {
      updateBadge();
      setSection(section.id);
    };
    const panel = el('section', {
      class: `attribute-workbench-panel is-${section.id}`,
      role: 'tabpanel',
    } as any);
    panel.append(section.content);
    tabs.append(button);
    panels.append(panel);
    tabBySection.set(section.id, button);
    panelBySection.set(section.id, panel);
  }

  const updateLessons = (lessons: TutorialLessonState[]): void => {
    const completed = lessons.filter((lesson) => lesson.done).length;
    progress.textContent = `${completed} / ${lessons.length}`;
    lessonList.replaceChildren();
    lessons.forEach((lesson, index) => {
      const button = el('button', {
        class: `attribute-training-lesson${lesson.done ? ' is-done' : ''}${lesson.active ? ' is-active' : ''}`,
        type: 'button',
        title: lesson.summary,
      }) as HTMLButtonElement;
      button.append(
        el('span', { class: 'attribute-training-number', text: lesson.done ? '✓' : String(index + 1) }),
        el('span', { class: 'attribute-training-copy' }, [
          el('strong', { text: lesson.label }),
          ...(lesson.active ? [el('small', { text: '正在练习' })] : []),
        ]),
      );
      button.onclick = () => {
        if (lesson.section) setSection(lesson.section);
        options.onLessonClick?.(lesson.id);
      };
      lessonList.append(el('li', {}, [button]));
    });
  };

  main.append(options.header, tabs, panels, options.footer);
  element.append(rail, main);
  updateLessons(options.lessons);
  setTrainingVisible(options.trainingVisible);
  setSection(options.initialSection);
  return {
    element,
    activeSection: () => currentSection,
    setSection,
    updateLessons,
    refreshBadges: () => badgeRefreshers.forEach((refresh) => refresh()),
    setTrainingVisible,
  };
}
