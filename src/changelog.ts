import changelogData from '../gift-panel-changelog.json';

export type ChangelogVisual = 'scene' | 'broadcast';

export interface ChangelogHighlight {
  label: string;
  title: string;
  description: string;
}

export interface ChangelogRelease {
  version: string;
  date: string;
  title: string;
  summary: string;
  highlights: ChangelogHighlight[];
  visuals: ChangelogVisual[];
}

const VISUALS = new Set<ChangelogVisual>(['scene', 'broadcast']);

export function normalizeChangelogReleases(value: unknown): ChangelogRelease[] {
  const record = value && typeof value === 'object' ? value as Record<string, unknown> : {};
  const source = Array.isArray(value) ? value : record.releases;
  if (!Array.isArray(source)) return [];
  const releases: ChangelogRelease[] = [];
  for (const candidate of source) {
    if (!candidate || typeof candidate !== 'object') continue;
    const item = candidate as Record<string, unknown>;
    const version = normalizeChangelogVersion(typeof item.version === 'string' ? item.version : '');
    const date = typeof item.date === 'string' ? item.date.trim() : '';
    const title = typeof item.title === 'string' ? item.title.trim() : '';
    const summary = typeof item.summary === 'string' ? item.summary.trim() : '';
    if (!version || !date || !title || !summary || !Array.isArray(item.highlights)) continue;
    const highlights = item.highlights.flatMap((highlight): ChangelogHighlight[] => {
      if (!highlight || typeof highlight !== 'object') return [];
      const row = highlight as Record<string, unknown>;
      const label = typeof row.label === 'string' ? row.label.trim() : '';
      const highlightTitle = typeof row.title === 'string' ? row.title.trim() : '';
      const description = typeof row.description === 'string' ? row.description.trim() : '';
      return label && highlightTitle && description ? [{ label, title: highlightTitle, description }] : [];
    });
    const visuals = Array.isArray(item.visuals)
      ? item.visuals.filter((visual): visual is ChangelogVisual => typeof visual === 'string' && VISUALS.has(visual as ChangelogVisual))
      : [];
    releases.push({ version, date, title, summary, highlights, visuals });
  }
  return releases;
}

export const CHANGELOG_RELEASES: ChangelogRelease[] = normalizeChangelogReleases(changelogData);

export function mergeChangelogReleases(
  hosted: ChangelogRelease[],
  bundled: ChangelogRelease[] = CHANGELOG_RELEASES,
): ChangelogRelease[] {
  const merged = new Map<string, ChangelogRelease>();
  for (const release of bundled) merged.set(release.version, release);
  for (const release of hosted) merged.set(release.version, release);
  return Array.from(merged.values()).sort((left, right) => right.version.localeCompare(left.version, undefined, { numeric: true }));
}

export function normalizeChangelogVersion(version: string | undefined): string {
  return String(version ?? '').trim().replace(/^v/i, '');
}

export function latestChangelogRelease(releases: ChangelogRelease[] = CHANGELOG_RELEASES): ChangelogRelease {
  return releases[0] ?? CHANGELOG_RELEASES[0];
}

export function changelogReleaseForVersion(
  version: string | undefined,
  releases: ChangelogRelease[] = CHANGELOG_RELEASES,
): ChangelogRelease | undefined {
  const normalized = normalizeChangelogVersion(version);
  return releases.find((release) => release.version === normalized);
}

export function shouldShowChangelog(
  currentVersion: string | undefined,
  lastSeenVersion: string | undefined,
  releases: ChangelogRelease[] = CHANGELOG_RELEASES,
): boolean {
  const release = changelogReleaseForVersion(currentVersion, releases);
  if (!release) return false;
  return release.version !== normalizeChangelogVersion(lastSeenVersion);
}
