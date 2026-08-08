import type { TrainingTopicId, TutorialLesson } from './types';
import { HELP_ENTRIES } from './data/help-content';

export type TrainingTopicCategory = 'advanced' | 'troubleshooting';
export type TrainingCatalogCategory = 'all' | 'main' | TrainingTopicCategory;
export type TrainingEditorSection = 'overview' | 'rules' | 'timers' | 'output';

export type TrainingDestination =
  | { kind: 'editor'; section: TrainingEditorSection }
  | { kind: 'page'; selector: string };

export interface TrainingTopicDefinition {
  id: TrainingTopicId;
  category: TrainingTopicCategory;
  title: string;
  summary: string;
  keywords: string[];
  steps: string[];
  outcome: string;
  destination: TrainingDestination;
  actionLabel: string;
  requiresAttribute?: boolean;
}

export interface MainLessonDetail {
  steps: string[];
  outcome: string;
}

const lessonEntries = HELP_ENTRIES.filter((entry) => entry.training?.kind === 'lesson');

export const MAIN_LESSON_DETAILS = Object.fromEntries(lessonEntries.map((entry) => {
  const metadata = entry.training;
  if (!metadata || metadata.kind !== 'lesson') throw new Error(`帮助条目 ${entry.id} 缺少主线课程信息`);
  return [metadata.id, { steps: entry.steps, outcome: entry.outcome }];
})) as Record<TutorialLesson, MainLessonDetail>;

export const TRAINING_TOPICS: TrainingTopicDefinition[] = HELP_ENTRIES.flatMap((entry) => {
  const metadata = entry.training;
  if (!metadata || metadata.kind !== 'topic') return [];
  return [{
    id: metadata.id,
    category: metadata.category,
    title: entry.title,
    summary: entry.summary,
    keywords: entry.keywords,
    steps: entry.steps,
    outcome: entry.outcome,
    destination: metadata.destination,
    actionLabel: metadata.actionLabel,
    ...(metadata.requiresAttribute ? { requiresAttribute: true } : {}),
  }];
});

const TOPIC_IDS = new Set<TrainingTopicId>(TRAINING_TOPICS.map((topic) => topic.id));

export function normalizeTrainingTopicIds(value: unknown): TrainingTopicId[] {
  if (!Array.isArray(value)) return [];
  return Array.from(new Set(value.filter((item): item is TrainingTopicId => (
    typeof item === 'string' && TOPIC_IDS.has(item as TrainingTopicId)
  ))));
}

export function matchesTrainingTopic(topic: TrainingTopicDefinition, query: string): boolean {
  return matchesTrainingText(
    [topic.title, topic.summary, topic.outcome, ...topic.keywords, ...topic.steps],
    query,
  );
}

export function matchesTrainingText(values: string[], query: string): boolean {
  const tokens = query
    .trim()
    .toLocaleLowerCase('zh-CN')
    .split(/\s+/)
    .filter(Boolean);
  if (tokens.length === 0) return true;
  const haystack = values.join('\n').toLocaleLowerCase('zh-CN');
  return tokens.every((token) => haystack.includes(token));
}
