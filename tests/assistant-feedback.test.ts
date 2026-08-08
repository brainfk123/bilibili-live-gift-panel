import { describe, expect, it } from 'vitest';
import { buildAssistantFeedbackJSON } from '../src/ui/config/assistant-drawer';

describe('assistant feedback JSON', () => {
  it('records redaction status and keeps only the safe state summary', () => {
    const feedback = JSON.parse(buildAssistantFeedbackJSON({
      question: '怎么连接直播间？',
      answer: '填写房间号并连接。',
      sources: [{ id: 'lesson-room', title: '连接直播间', sourceLabel: '训练中心 · 连接直播间' }],
      appVersion: '0.2.4',
      modelVersion: 'qwen3-test',
      stateSummary: {
        appVersion: '0.2.4',
        connection: 'idle',
        roomConfigured: true,
        roomId: '123456',
        username: 'private-user',
        logs: ['secret'],
      },
    }));

    expect(feedback.redaction).toEqual({
      status: 'redacted',
      excludedFields: ['roomId', 'username', 'audienceData', 'cookies', 'fullConfig', 'logs'],
    });
    expect(feedback.stateSummary).toEqual({
      appVersion: '0.2.4',
      connection: 'idle',
      roomConfigured: true,
    });
    expect(feedback.sources).toEqual([
      { id: 'lesson-room', title: '连接直播间', sourceLabel: '训练中心 · 连接直播间' },
    ]);
  });
});
