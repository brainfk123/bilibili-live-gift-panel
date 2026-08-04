import { describe, expect, it } from 'vitest';
import { MAIN_LESSON_DETAILS, matchesTrainingTopic, normalizeTrainingTopicIds, TRAINING_TOPICS } from '../src/training';

describe('training catalog', () => {
  it('covers the complete main tutorial with concrete actions and outcomes', () => {
    expect(Object.keys(MAIN_LESSON_DETAILS)).toHaveLength(10);
    for (const detail of Object.values(MAIN_LESSON_DETAILS)) {
      expect(detail.steps.length).toBeGreaterThanOrEqual(3);
      expect(detail.outcome.length).toBeGreaterThan(10);
    }
  });

  it('offers advanced and troubleshooting topics that can be searched in plain language', () => {
    expect(TRAINING_TOPICS.filter((topic) => topic.category === 'advanced').length).toBeGreaterThanOrEqual(8);
    expect(TRAINING_TOPICS.filter((topic) => topic.category === 'troubleshooting').length).toBeGreaterThanOrEqual(3);
    expect(TRAINING_TOPICS.filter((topic) => matchesTrainingTopic(topic, 'OBS 没更新')).map((topic) => topic.id))
      .toContain('obs-no-change');
    expect(TRAINING_TOPICS.filter((topic) => matchesTrainingTopic(topic, '盲盒')).map((topic) => topic.id))
      .toContain('blind-box');
  });

  it('normalizes persisted topic progress and drops unknown values', () => {
    expect(normalizeTrainingTopicIds(['blind-box', 'blind-box', 'unknown', 3, 'timer-skipped']))
      .toEqual(['blind-box', 'timer-skipped']);
  });
});
