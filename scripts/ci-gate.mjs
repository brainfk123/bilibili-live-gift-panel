import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';

const allowedResults = new Set(['success', 'failure', 'cancelled', 'skipped']);

export function evaluateGate(input) {
  const failures = [];
  for (const name of ['scope', 'hosted', 'mysql', 'windows']) {
    if (!allowedResults.has(input[name])) failures.push(name + ':invalid-result');
  }
  if (input.scope !== 'success') failures.push('scope:' + input.scope);
  if (input.hosted !== 'success') failures.push('hosted:' + input.hosted);
  if (input.expectMySQL ? input.mysql !== 'success' : !['success', 'skipped'].includes(input.mysql)) failures.push('mysql:' + input.mysql);
  if (input.expectWindows ? input.windows !== 'success' : !['success', 'skipped'].includes(input.windows)) failures.push('windows:' + input.windows);
  return { ok: failures.length === 0, failures: [...new Set(failures)] };
}

const isMain = process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url;
if (isMain) {
  const input = {
    scope: process.env.CI_SCOPE_RESULT,
    hosted: process.env.CI_HOSTED_RESULT,
    mysql: process.env.CI_MYSQL_RESULT,
    windows: process.env.CI_WINDOWS_RESULT,
    expectMySQL: process.env.CI_EXPECT_MYSQL === 'true',
    expectWindows: process.env.CI_EXPECT_WINDOWS === 'true',
  };
  const result = evaluateGate(input);
  console.log(JSON.stringify(result));
  if (!result.ok) process.exitCode = 1;
}
