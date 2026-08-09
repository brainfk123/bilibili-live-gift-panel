const GOLD_SEEDS_PER_YUAN = 1_000;

const exactYuanFormatter = new Intl.NumberFormat('zh-CN', {
  maximumFractionDigits: 3,
});

const compactYuanFormatter = new Intl.NumberFormat('zh-CN', {
  notation: 'compact',
  maximumFractionDigits: 1,
});

export function goldSeedsFromYuan(value: number): number {
  return Math.round(Number(value || 0) * GOLD_SEEDS_PER_YUAN);
}

export function formatYuanFromGoldSeeds(value: number): string {
  return `${exactYuanFormatter.format(toYuan(value))} 元`;
}

export function formatSignedYuanFromGoldSeeds(value: number): string {
  const normalized = Number(value || 0);
  const sign = normalized > 0 ? '+' : normalized < 0 ? '-' : '';
  return `${sign}${formatYuanFromGoldSeeds(Math.abs(normalized))}`;
}

export function formatCompactYuanFromGoldSeeds(value: number): string {
  const yuan = toYuan(value);
  if (Math.abs(yuan) < 10_000) return `${exactYuanFormatter.format(yuan)} 元`;
  return `${compactYuanFormatter.format(yuan)}元`;
}

function toYuan(value: number): number {
  return Number(value || 0) / GOLD_SEEDS_PER_YUAN;
}
