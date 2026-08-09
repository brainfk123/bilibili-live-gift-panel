/** Formats a validated, non-negative integer duration using natural Chinese units. */
export function formatDurationZh(seconds: number): string {
  const hours = Math.floor(seconds / 3_600);
  const minutes = Math.floor((seconds % 3_600) / 60);
  const remainder = seconds % 60;
  const parts: string[] = [];
  if (hours > 0) parts.push(`${hours}时`);
  if (minutes > 0) parts.push(`${minutes}分`);
  if (remainder > 0) parts.push(`${remainder}秒`);
  return parts.join('') || '0秒';
}
