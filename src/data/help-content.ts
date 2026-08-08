import rawHelpEntries from './help-content.json';
import type { TrainingTopicId, TutorialLesson } from '../types';
import type { ConfigPageId } from '../ui/config/config-route';

export type HelpCategory =
  | 'getting-started'
  | 'connection'
  | 'attributes'
  | 'gifts'
  | 'timers'
  | 'obs'
  | 'auth'
  | 'data'
  | 'updates'
  | 'troubleshooting'
  | 'activities';

export type HelpAction =
  | { kind: 'config-page'; target: ConfigPageId; label: string }
  | { kind: 'training-topic'; target: TrainingTopicId; label: string };

type TrainingMetadata =
  | { kind: 'lesson'; id: TutorialLesson }
  | {
    kind: 'topic';
    id: TrainingTopicId;
    category: 'advanced' | 'troubleshooting';
    destination: { kind: 'editor'; section: 'overview' | 'rules' | 'timers' | 'output' } | { kind: 'page'; selector: string };
    actionLabel: string;
    requiresAttribute?: boolean;
  };

export interface HelpEntry {
  id: string;
  category: HelpCategory;
  title: string;
  summary: string;
  questionVariants: string[];
  keywords: string[];
  steps: string[];
  outcome: string;
  sourceLabel: string;
  action?: HelpAction;
  training?: TrainingMetadata;
}

export const HELP_ENTRIES = rawHelpEntries as HelpEntry[];

export const HELP_GOLDEN_QUERIES = HELP_ENTRIES.flatMap((entry) => (
  entry.questionVariants.map((question) => ({ question, expectedEntryId: entry.id }))
));

export function helpEntryById(id: string): HelpEntry | undefined {
  return HELP_ENTRIES.find((entry) => entry.id === id);
}
