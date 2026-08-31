import { execFileSync } from 'node:child_process';
import { appendFileSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { dirname, resolve } from 'node:path';

export const WINDOWS_LEVELS = Object.freeze(['skip', 'shared', 'desktop', 'desktop-high-risk']);
const rank = new Map(WINDOWS_LEVELS.map((level, index) => [level, index]));

const rules = [
  { level: 'skip', patterns: [/^(?:docs\/|acceptance\/|.*\.md$)/, /^(?:src\/hosted\/|goserver\/(?:cmd\/hosted|internal\/hosted)\/|deploy\/hosted\/)/, /^tests\/hosted-.*\.test\.ts$/, /^tests\/(?:development-workflow|ui-parity-requirements|exe-retirement-contract)\.test\.ts$/, /^scripts\/(?:build-hosted|test-hosted-mysql)\.mjs$/, /^(?:hosted|obs)\.html$/, /^vite\.hosted\.config\.ts$/] },
  { level: 'shared', patterns: [/^goserver\/internal\/gameplay\//, /^goserver\/gameplay_adapter(?:_test)?\.go$/, /^src\/migration(?:-gameplay-units)?\.ts$/, /^tests\/(?:migration|migration-gameplay-units)\.test\.ts$/, /^tests\/fixtures\/online-migration-/] },
  { level: 'desktop', patterns: [/^src\/(?!hosted\/|migration(?:-gameplay-units)?\.ts$)/, /^tests\/(?!hosted-|(?:migration|migration-gameplay-units)\.test\.ts$|fixtures\/online-migration-)/, /^goserver\/(?!gameplay_adapter(?:_test)?\.go$)[^/]+\.go$/, /^(?:index\.html|vite\.config\.ts)$/] },
  { level: 'desktop-high-risk', patterns: [/^\.github\/workflows\/(?:ci|release)\.yml$/, /^updateapi\//, /^deploy\/update-api\//, /^goserver\/(?:ffmpeg\/|.*_windows(?:_test)?\.go$|auto_update|gift_clip|tray_|atomic_replace|auth_protection)/, /^scripts\/(?:build-go|build-ffmpeg|package-ffmpeg|verify-ffmpeg|sign-evsign|ffmpeg-)/, /^(?:package|package-lock|tsconfig)\.json$/] },
];

export function parseNameStatusZ(output) {
  const fields = String(output ?? '').split('\0');
  if (fields.at(-1) === '') fields.pop();
  const changes = [];
  for (let index = 0; index < fields.length;) {
    const status = fields[index++];
    if (!/^(?:[RC]\d{0,3}|[ACDMRTUXB])$/.test(status ?? '')) return [];
    const path = fields[index++];
    if (!path) return [];
    if (/^[RC]/.test(status)) {
      const destination = fields[index++];
      if (!destination) return [];
      changes.push({ status, path, destination });
    } else changes.push({ status, path });
  }
  return changes;
}

export function readGitChanges(baseSHA, headSHA, cwd = process.cwd()) {
  if (!/^[0-9a-f]{4,64}$/i.test(baseSHA ?? '') || !/^[0-9a-f]{4,64}$/i.test(headSHA ?? '')) {
    throw new Error('baseSHA and headSHA must be hexadecimal git SHAs');
  }
  let output;
  try {
    const repository = resolve(cwd);
    output = execFileSync('git', ['-c', `safe.directory=${repository.replaceAll('\\', '/')}`, '-C', repository, 'diff', '--name-status', '-z', '--find-renames', `${baseSHA}...${headSHA}`], { cwd: repository, encoding: 'utf8' });
  } catch {
    return [{ status: 'M', path: '<unknown-git-diff>' }];
  }
  const changes = parseNameStatusZ(output);
  return changes.length ? changes : [{ status: 'M', path: '<unknown-git-diff>' }];
}

export function classifyChanges(changes) {
  let windowsLevel = 'skip';
  let runMySQL = false;
  const reasons = [];
  for (const change of changes) {
    for (const path of [change.path, change.destination].filter(Boolean)) {
      const matches = rules.filter((rule) => rule.patterns.some((pattern) => pattern.test(path)));
      const level = matches.reduce((best, item) => rank.get(item.level) > rank.get(best) ? item.level : best, matches.length ? 'skip' : 'desktop-high-risk');
      if (rank.get(level) > rank.get(windowsLevel)) windowsLevel = level;
      if (/^goserver\/internal\/hosted\/store\/mysqlstore\/|^deploy\/hosted\/docker-compose\.test\.yml$|^scripts\/test-hosted-mysql\.mjs$/.test(path)) runMySQL = true;
      reasons.push({ path, level, reason: matches.length ? 'matched-explicit-rule' : 'unknown-path-fail-closed' });
    }
  }
  return { windowsLevel, runWindows: windowsLevel !== 'skip', runMySQL, reasons };
}

function writeGitHubOutput(decision, outputPath) {
  const lines = [`windows_level=${decision.windowsLevel}`, `run_windows=${decision.runWindows}`, `run_mysql=${decision.runMySQL}`, `reasons_json=${JSON.stringify(decision.reasons)}`];
  // Append through the file API so changed paths are never interpreted by a shell.
  appendFileSync(outputPath, `${lines.join('\n')}\n`, 'utf8');
}

const isMain = process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url;
if (isMain) {
  const [baseSHA, headSHA] = process.argv.slice(2);
  const changes = readGitChanges(baseSHA, headSHA, dirname(fileURLToPath(import.meta.url)) + '/..');
  const decision = classifyChanges(changes);
  if (process.env.GITHUB_OUTPUT) writeGitHubOutput(decision, process.env.GITHUB_OUTPUT);
  else console.log(JSON.stringify(decision));
}
