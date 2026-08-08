import { describe, expect, it } from 'vitest';
import { HELP_ENTRIES, HELP_GOLDEN_QUERIES } from '../src/data/help-content';
import { TRAINING_TOPICS, MAIN_LESSON_DETAILS } from '../src/training';

const CONFIG_PAGE_IDS = new Set(['overview', 'attributes', 'activities', 'kpi', 'obs', 'data']);
const TRAINING_TOPIC_IDS = new Set(TRAINING_TOPICS.map((topic) => topic.id));

describe('authoritative help content', () => {
  it('provides a structured, unique and useful answer for every entry', () => {
    expect(HELP_ENTRIES.length).toBeGreaterThanOrEqual(30);
    expect(new Set(HELP_ENTRIES.map((entry) => entry.id)).size).toBe(HELP_ENTRIES.length);
    for (const entry of HELP_ENTRIES) {
      expect(entry.title.length).toBeGreaterThan(3);
      expect(entry.summary.length).toBeGreaterThan(8);
      expect(entry.questionVariants.length).toBeGreaterThanOrEqual(4);
      expect(entry.keywords.length).toBeGreaterThanOrEqual(4);
      expect(entry.steps.length).toBeGreaterThanOrEqual(3);
      expect(entry.outcome.length).toBeGreaterThan(8);
      expect(entry.sourceLabel.length).toBeGreaterThan(4);
    }
  });

  it('ships at least 120 labeled retrieval queries', () => {
    expect(HELP_GOLDEN_QUERIES.length).toBeGreaterThanOrEqual(120);
    const entryIds = new Set(HELP_ENTRIES.map((entry) => entry.id));
    expect(HELP_GOLDEN_QUERIES.every((query) => entryIds.has(query.expectedEntryId))).toBe(true);
    expect(HELP_GOLDEN_QUERIES.every((query) => query.question.trim().length >= 4)).toBe(true);
  });

  it('allows only typed navigation targets', () => {
    for (const entry of HELP_ENTRIES) {
      if (!entry.action) continue;
      if (entry.action.kind === 'config-page') expect(CONFIG_PAGE_IDS.has(entry.action.target)).toBe(true);
      else expect(TRAINING_TOPIC_IDS.has(entry.action.target)).toBe(true);
    }
  });

  it('keeps the training center derived from the same help entries', () => {
    expect(Object.keys(MAIN_LESSON_DETAILS)).toHaveLength(12);
    expect(TRAINING_TOPICS).toHaveLength(13);
    for (const topic of TRAINING_TOPICS) {
      const entry = HELP_ENTRIES.find((candidate) => candidate.training?.kind === 'topic' && candidate.training.id === topic.id);
      expect(entry?.title).toBe(topic.title);
      expect(entry?.steps).toEqual(topic.steps);
    }
  });
});
