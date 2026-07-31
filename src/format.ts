import { Attribute, AppState, DayStats } from './types';

export function formatValue(value: number, attr: Attribute): string {
  switch (attr.format) {
    case 'hhmmss':
      return formatSeconds(value);
    case 'number':
      return formatNumber(value, attr.decimals);
    case 'suffix':
      return `${formatNumber(value, attr.decimals)} ${attr.suffix}`.trim();
    default:
      return String(value);
  }
}

export function formatSeconds(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds));
  const days = Math.floor(s / 86400);
  const hours = Math.floor((s % 86400) / 3600);
  const minutes = Math.floor((s % 3600) / 60);
  const seconds = s % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  const hms = `${pad(hours)}:${pad(minutes)}:${pad(seconds)}`;
  return days > 0 ? `${days}天 ${hms}` : hms;
}

export function formatNumber(value: number, decimals: number): string {
  return value.toLocaleString('zh-CN', {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

export function todayStr(): string {
  const d = new Date();
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

export function getDayStats(state: AppState): DayStats {
  const date = todayStr();
  let stats = state.stats[date];
  if (!stats) {
    stats = { date, giftTotals: {}, ruleTriggers: {} };
    state.stats[date] = stats;
  }
  return stats;
}
