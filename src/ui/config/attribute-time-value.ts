export type AttributeTimeParseResult =
  | { ok: true; seconds: number }
  | { ok: false; message: string };

const INTEGER_SEGMENT = /^\d+$/;
const MAX_SAFE_SECONDS = Number.MAX_SAFE_INTEGER;

export function parseAttributeTimeValue(input: string): AttributeTimeParseResult {
  const trimmed = input.trim();
  if (!trimmed) return { ok: false, message: '请输入时间' };

  const segments = trimmed.split(':');
  if (segments.length !== 3 && segments.length !== 4) {
    return { ok: false, message: '时间格式应为 时:分:秒 或 天:时:分:秒' };
  }
  if (!segments.every((segment) => INTEGER_SEGMENT.test(segment))) {
    return { ok: false, message: '时间每一段都必须是非负整数' };
  }

  const values = segments.map(Number);
  const [days, hours, minutes, seconds] = values.length === 4
    ? values
    : [0, values[0], values[1], values[2]];
  if (minutes > 59) return { ok: false, message: '分钟必须在 0–59 之间' };
  if (seconds > 59) return { ok: false, message: '秒必须在 0–59 之间' };
  if (segments.length === 4 && hours > 23) {
    return { ok: false, message: '四段时间中的小时必须在 0–23 之间' };
  }

  const total = days * 86400 + hours * 3600 + minutes * 60 + seconds;
  if (!Number.isSafeInteger(total) || total > MAX_SAFE_SECONDS) {
    return { ok: false, message: '时间超出支持范围' };
  }
  return { ok: true, seconds: total };
}

export function formatAttributeTimeValue(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds));
  const secondsPart = total % 60;
  const minutesPart = Math.floor(total / 60) % 60;
  const hours = Math.floor(total / 3600);
  if (total < 86400) {
    return `${String(hours).padStart(2, '0')}:${String(minutesPart).padStart(2, '0')}:${String(secondsPart).padStart(2, '0')}`;
  }
  const days = Math.floor(hours / 24);
  const hoursPart = hours % 24;
  return `${days}:${String(hoursPart).padStart(2, '0')}:${String(minutesPart).padStart(2, '0')}:${String(secondsPart).padStart(2, '0')}`;
}

export function adjustAttributeTimeValue(input: string, deltaSeconds: number): AttributeTimeParseResult {
  const parsed = parseAttributeTimeValue(input);
  if (!parsed.ok) return parsed;
  const seconds = Math.max(0, parsed.seconds + deltaSeconds);
  return Number.isSafeInteger(seconds) && seconds <= MAX_SAFE_SECONDS
    ? { ok: true, seconds }
    : { ok: false, message: '时间超出支持范围' };
}
