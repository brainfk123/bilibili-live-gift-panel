import { describe, expect, it } from 'vitest';
import {
  adjustAttributeTimeValue,
  formatAttributeTimeValue,
  parseAttributeTimeValue,
} from '../src/ui/config/attribute-time-value';

describe('attribute timer value input', () => {
  it.each([
    ['02:30:45', 9045],
    ['36:00:00', 129600],
    ['1:02:30:45', 95445],
    ['0:23:59:59', 86399],
  ])('parses %s', (input, seconds) => {
    expect(parseAttributeTimeValue(input)).toEqual({ ok: true, seconds });
  });

  it.each([
    ['', '请输入时间'],
    ['1:2', '时间格式应为 时:分:秒 或 天:时:分:秒'],
    ['1::02', '时间每一段都必须是非负整数'],
    ['-1:00:00', '时间每一段都必须是非负整数'],
    ['1.5:00:00', '时间每一段都必须是非负整数'],
    ['1:60:00', '分钟必须在 0–59 之间'],
    ['1:00:60', '秒必须在 0–59 之间'],
    ['1:24:00:00', '四段时间中的小时必须在 0–23 之间'],
    ['999999999999999999:00:00', '时间超出支持范围'],
  ])('rejects %s', (input, message) => {
    expect(parseAttributeTimeValue(input)).toEqual({ ok: false, message });
  });

  it.each([
    [0, '00:00:00'],
    [9045, '02:30:45'],
    [129600, '1:12:00:00'],
  ])('formats %d seconds', (seconds, text) => {
    expect(formatAttributeTimeValue(seconds)).toBe(text);
  });

  it('applies shortcuts and clamps at zero', () => {
    expect(adjustAttributeTimeValue('00:00:20', -30)).toEqual({ ok: true, seconds: 0 });
    expect(adjustAttributeTimeValue('00:00:20', 600)).toEqual({ ok: true, seconds: 620 });
    expect(adjustAttributeTimeValue('bad', 30)).toEqual({ ok: false, message: '时间格式应为 时:分:秒 或 天:时:分:秒' });
  });
});
