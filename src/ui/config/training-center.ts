import type { TrainingTopicId, TutorialLesson } from '../../types';
import {
  MAIN_LESSON_DETAILS,
  matchesTrainingTopic,
  matchesTrainingText,
  TRAINING_TOPICS,
  type TrainingCatalogCategory,
  type TrainingTopicDefinition,
} from '../../training';
import { el } from '../common';
import type { TutorialLessonState } from './wizard';

interface TrainingCenterOptions {
  lessons: TutorialLessonState[];
  completedTopics: TrainingTopicId[];
  resumeLesson: TutorialLesson;
  hasAttribute: boolean;
  onClose: () => void;
  onBeginLesson: (lesson: TutorialLesson) => void;
  onOpenTopic: (topic: TrainingTopicDefinition) => void;
  onTopicCompletionChange: (topic: TrainingTopicId, complete: boolean) => void;
  onReset: () => void;
}

type CatalogSelection =
  | { kind: 'lesson'; id: TutorialLesson }
  | { kind: 'topic'; id: TrainingTopicId };

const CATEGORY_LABELS: Record<Exclude<TrainingCatalogCategory, 'all'>, string> = {
  main: '实战主线',
  advanced: '进阶玩法',
  troubleshooting: '排查问题',
};

function selectionKey(selection: CatalogSelection): string {
  return `${selection.kind}:${selection.id}`;
}

export function createTrainingCenter(options: TrainingCenterOptions): HTMLElement {
  const overlay = el('div', { class: 'overlay training-center-overlay' });
  const dialog = el('section', {
    class: 'card training-center',
    role: 'dialog',
    ariaLabel: '直播互动训练中心',
  } as any);
  const completedTopicSet = new Set(options.completedTopics);
  const firstIncomplete = options.lessons.find((lesson) => !lesson.done) ?? options.lessons[0];
  let selected: CatalogSelection = { kind: 'lesson', id: firstIncomplete.id };
  let category: TrainingCatalogCategory = 'all';
  let query = '';

  const closeButton = el('button', { class: 'modal-close', type: 'button', text: '×', ariaLabel: '关闭' } as any) as HTMLButtonElement;
  closeButton.onclick = options.onClose;
  const mainDone = options.lessons.filter((lesson) => lesson.done).length;
  const mainProgress = el('span');
  const topicProgress = el('span');
  const summary = el('div', { class: 'training-center-summary' }, [
    mainProgress,
    topicProgress,
  ]);
  const header = el('header', { class: 'modal-header training-center-header' }, [
    el('div', {}, [
      el('span', { class: 'section-kicker', text: '随时搜索、重练或退出' }),
      el('h2', { text: '直播互动训练中心' }),
      el('p', { text: '主线会做出一台可直播的加班机；进阶课解释更多玩法，排错课直接告诉你先检查哪里。' }),
      summary,
    ]),
    closeButton,
  ]);

  const search = el('input', {
    class: 'field-input training-center-search',
    type: 'search',
    placeholder: '搜索：盲盒、随机、定时器没运行、OBS 没更新…',
    ariaLabel: '搜索训练课程',
  } as any) as HTMLInputElement;
  const filters = el('nav', { class: 'training-center-filters', ariaLabel: '课程分类' } as any);
  const filterButtons = new Map<TrainingCatalogCategory, HTMLButtonElement>();
  const filterDefinitions: Array<[TrainingCatalogCategory, string]> = [
    ['all', '全部'],
    ['main', '实战主线'],
    ['advanced', '进阶玩法'],
    ['troubleshooting', '排查问题'],
  ];
  filterDefinitions.forEach(([id, label]) => {
    const button = el('button', { class: 'training-center-filter', type: 'button', text: label }) as HTMLButtonElement;
    button.onclick = () => {
      category = id;
      renderAll();
    };
    filterButtons.set(id, button);
    filters.append(button);
  });
  const toolbar = el('div', { class: 'training-center-toolbar' }, [search, filters]);

  const catalog = el('div', { class: 'training-center-catalog' });
  const detail = el('aside', { class: 'training-center-detail', ariaLive: 'polite' } as any);
  const body = el('div', { class: 'training-center-body' }, [catalog, detail]);

  const resetButton = el('button', { class: 'btn ghost', type: 'button', text: '重置主线进度' }) as HTMLButtonElement;
  resetButton.onclick = options.onReset;
  const resumeButton = el('button', { class: 'btn', type: 'button', text: '继续主线训练' }) as HTMLButtonElement;
  resumeButton.onclick = () => options.onBeginLesson(options.resumeLesson);
  const footer = el('footer', { class: 'modal-actions training-center-actions' }, [
    el('span', { text: '退出训练不会丢失已经保存的配置。' }),
    resetButton,
    resumeButton,
  ]);

  function filteredSelections(): CatalogSelection[] {
    const lessons = category === 'all' || category === 'main'
      ? options.lessons.filter((lesson) => {
        const detailText = MAIN_LESSON_DETAILS[lesson.id];
        return matchesTrainingText([lesson.label, lesson.summary, detailText.outcome, ...detailText.steps], query);
      }).map((lesson) => ({ kind: 'lesson' as const, id: lesson.id }))
      : [];
    const topics = category === 'all' || category === 'advanced' || category === 'troubleshooting'
      ? TRAINING_TOPICS
        .filter((topic) => category === 'all' || topic.category === category)
        .filter((topic) => matchesTrainingTopic(topic, query))
        .map((topic) => ({ kind: 'topic' as const, id: topic.id }))
      : [];
    return [...lessons, ...topics];
  }

  function renderCatalog(): void {
    for (const [id, button] of filterButtons) {
      const active = id === category;
      button.classList.toggle('is-active', active);
      button.setAttribute('aria-pressed', String(active));
    }
    const visible = filteredSelections();
    if (!visible.some((item) => selectionKey(item) === selectionKey(selected))) {
      selected = visible[0] ?? selected;
    }
    catalog.replaceChildren();
    if (visible.length === 0) {
      catalog.append(el('div', { class: 'training-center-empty' }, [
        el('strong', { text: '没有找到相关课程' }),
        el('span', { text: '换一个更短的关键词试试，例如“盲盒”“定时器”或“OBS”。' }),
      ]));
      return;
    }

    const groups: Array<{ category: Exclude<TrainingCatalogCategory, 'all'>; items: CatalogSelection[] }> = [
      { category: 'main', items: visible.filter((item) => item.kind === 'lesson') },
      { category: 'advanced', items: visible.filter((item) => item.kind === 'topic' && TRAINING_TOPICS.find((topic) => topic.id === item.id)?.category === 'advanced') },
      { category: 'troubleshooting', items: visible.filter((item) => item.kind === 'topic' && TRAINING_TOPICS.find((topic) => topic.id === item.id)?.category === 'troubleshooting') },
    ];
    groups.filter((group) => group.items.length > 0).forEach((group) => {
      const section = el('section', { class: `training-center-group is-${group.category}` });
      section.append(el('div', { class: 'training-center-group-heading' }, [
        el('strong', { text: CATEGORY_LABELS[group.category] }),
        el('span', { text: `${group.items.length} 课` }),
      ]));
      const list = el('div', { class: 'training-center-course-list' });
      group.items.forEach((item) => list.append(renderCatalogCard(item)));
      section.append(list);
      catalog.append(section);
    });
  }

  function renderCatalogCard(item: CatalogSelection): HTMLElement {
    const active = selectionKey(item) === selectionKey(selected);
    if (item.kind === 'lesson') {
      const index = options.lessons.findIndex((lesson) => lesson.id === item.id);
      const lesson = options.lessons[index];
      const button = el('button', {
        class: `training-center-course${lesson.done ? ' is-done' : ''}${active ? ' is-active' : ''}`,
        type: 'button',
      }) as HTMLButtonElement;
      button.dataset.trainingItem = selectionKey(item);
      button.append(
        el('span', { class: 'training-center-course-number', text: lesson.done ? '✓' : String(index + 1).padStart(2, '0') }),
        el('span', { class: 'training-center-course-copy' }, [
          el('strong', { text: lesson.label }),
          el('small', { text: lesson.summary }),
        ]),
      );
      button.onclick = () => {
        selected = item;
        renderAll();
      };
      return button;
    }
    const topic = TRAINING_TOPICS.find((candidate) => candidate.id === item.id)!;
    const done = completedTopicSet.has(topic.id);
    const button = el('button', {
      class: `training-center-course${done ? ' is-done' : ''}${active ? ' is-active' : ''}`,
      type: 'button',
    }) as HTMLButtonElement;
    button.dataset.trainingItem = selectionKey(item);
    button.append(
      el('span', { class: 'training-center-course-number', text: done ? '✓' : topic.category === 'advanced' ? '进' : '?' }),
      el('span', { class: 'training-center-course-copy' }, [
        el('strong', { text: topic.title }),
        el('small', { text: topic.summary }),
      ]),
    );
    button.onclick = () => {
      selected = item;
      renderAll();
    };
    return button;
  }

  function renderDetail(): void {
    detail.replaceChildren();
    const visible = filteredSelections();
    if (visible.length === 0) {
      detail.append(el('div', { class: 'training-center-detail-empty', text: '搜索到课程后，这里会显示具体步骤。' }));
      return;
    }
    if (selected.kind === 'lesson') {
      const lesson = options.lessons.find((candidate) => candidate.id === selected.id)!;
      const detailCopy = MAIN_LESSON_DETAILS[lesson.id];
      const begin = el('button', { class: 'btn', type: 'button', text: lesson.done ? '重新练习这一关' : '开始这一关' }) as HTMLButtonElement;
      begin.onclick = () => options.onBeginLesson(lesson.id);
      detail.append(
        detailHeader('实战主线', lesson.label, lesson.summary, lesson.done),
        renderSteps(detailCopy.steps),
        outcomeCard(detailCopy.outcome),
        el('div', { class: 'training-center-detail-actions' }, [begin]),
      );
      return;
    }

    const topic = TRAINING_TOPICS.find((candidate) => candidate.id === selected.id)!;
    const done = completedTopicSet.has(topic.id);
    const open = el('button', {
      class: 'btn',
      type: 'button',
      text: topic.requiresAttribute && !options.hasAttribute ? '先创建一个属性' : topic.actionLabel,
    }) as HTMLButtonElement;
    open.onclick = () => {
      if (topic.requiresAttribute && !options.hasAttribute) options.onBeginLesson('attribute');
      else options.onOpenTopic(topic);
    };
    const complete = el('button', {
      class: 'btn ghost training-topic-complete',
      type: 'button',
      text: done ? '标为未掌握' : '标记已掌握',
    }) as HTMLButtonElement;
    complete.onclick = () => {
      const next = !completedTopicSet.has(topic.id);
      if (next) completedTopicSet.add(topic.id);
      else completedTopicSet.delete(topic.id);
      options.onTopicCompletionChange(topic.id, next);
      renderAll();
    };
    detail.append(
      detailHeader(CATEGORY_LABELS[topic.category], topic.title, topic.summary, done),
      renderSteps(topic.steps),
      outcomeCard(topic.outcome),
      el('div', { class: 'training-center-detail-actions' }, [complete, open]),
    );
  }

  function detailHeader(kicker: string, title: string, description: string, done: boolean): HTMLElement {
    return el('header', { class: 'training-center-detail-header' }, [
      el('div', { class: 'training-center-detail-meta' }, [
        el('span', { class: 'training-center-detail-kicker', text: kicker }),
        ...(done ? [el('span', { class: 'training-center-complete-badge', text: '已完成' })] : []),
      ]),
      el('h3', { text: title }),
      el('p', { text: description }),
    ]);
  }

  function renderSteps(steps: string[]): HTMLElement {
    return el('section', { class: 'training-center-detail-section' }, [
      el('strong', { text: '跟着做' }),
      el('ol', {}, steps.map((step) => el('li', { text: step }))),
    ]);
  }

  function outcomeCard(outcome: string): HTMLElement {
    return el('section', { class: 'training-center-outcome' }, [
      el('span', { text: '学完你会明白' }),
      el('p', { text: outcome }),
    ]);
  }

  function renderAll(): void {
    mainProgress.textContent = `主线 ${mainDone}/${options.lessons.length}`;
    topicProgress.textContent = `专题 ${completedTopicSet.size}/${TRAINING_TOPICS.length}`;
    renderCatalog();
    renderDetail();
  }

  search.oninput = () => {
    query = search.value;
    renderAll();
  };

  let pointerStartedOutside = false;
  overlay.onpointerdown = (event) => {
    pointerStartedOutside = event.target === overlay;
  };
  overlay.onclick = (event) => {
    const shouldClose = pointerStartedOutside && event.target === overlay;
    pointerStartedOutside = false;
    if (shouldClose) options.onClose();
  };

  dialog.append(header, toolbar, body, footer);
  overlay.append(dialog);
  renderAll();
  return overlay;
}
