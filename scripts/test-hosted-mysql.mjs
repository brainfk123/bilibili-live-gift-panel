import { randomBytes } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { dirname, resolve } from 'node:path';

const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const composeArguments = ['compose', '-p', 'gift-panel-hosted-test', '-f', 'deploy/hosted/docker-compose.test.yml'];
const sensitiveEnvironmentNames = new Set([
  'HOSTED_MYSQL_TEST_DSN',
  'HOSTED_MYSQL_TEST_REQUIRED',
  'HOSTED_MYSQL_TEST_ROOT_PASSWORD',
]);

function sanitizedEnvironment(environment) {
  const result = {};
  for (const [key, value] of Object.entries(environment)) {
    if (!sensitiveEnvironmentNames.has(key.toUpperCase())) result[key] = value;
  }
  return result;
}

function executeCommand(command, args, options) {
  const result = spawnSync(command, args, {
    cwd: projectRoot,
    env: options.env,
    shell: false,
    stdio: 'inherit',
  });
  return { status: result.status };
}

function commandFailed(result) {
  return result == null || result.status !== 0;
}

export function runHostedMySQLTests(options = {}) {
  const execute = options.execute ?? executeCommand;
  const password = (options.createPassword ?? (() => randomBytes(24).toString('base64url')))();
  if (!/^[A-Za-z0-9_-]{32}$/.test(password)) {
    throw new Error('hosted MySQL integration tests failed');
  }
  const baseEnvironment = sanitizedEnvironment(options.environment ?? process.env);
  const composeEnvironment = { ...baseEnvironment, HOSTED_MYSQL_TEST_ROOT_PASSWORD: password };
  const goEnvironment = {
    ...baseEnvironment,
    HOSTED_MYSQL_TEST_REQUIRED: '1',
    HOSTED_MYSQL_TEST_DSN: `root:${password}@tcp(127.0.0.1:13306)/?multiStatements=false&parseTime=true`,
  };
  let failure = null;
  try {
    if (commandFailed(execute('docker', [...composeArguments, 'up', '-d', '--wait', '--wait-timeout', '120'], { env: composeEnvironment }))) {
      failure = new Error('hosted MySQL integration tests failed');
    } else if (commandFailed(execute('go', ['-C', 'goserver', 'test', '-tags=integration', './internal/hosted/store/mysqlstore', '-run', '^TestIntegration', '-count=1'], { env: goEnvironment }))) {
      failure = new Error('hosted MySQL integration tests failed');
    }
  } catch {
    failure = new Error('hosted MySQL integration tests failed');
  } finally {
    try {
      if (commandFailed(execute('docker', [...composeArguments, 'down', '--volumes', '--remove-orphans'], { env: composeEnvironment })) && failure == null) {
        failure = new Error('hosted MySQL integration tests failed');
      }
    } catch {
      if (failure == null) failure = new Error('hosted MySQL integration tests failed');
    }
  }
  if (failure != null) throw failure;
}

const isMain = process.argv[1] != null && pathToFileURL(resolve(process.argv[1])).href === import.meta.url;
if (isMain) {
  try {
    runHostedMySQLTests();
  } catch {
    console.error('hosted MySQL integration tests failed');
    process.exitCode = 1;
  }
}
