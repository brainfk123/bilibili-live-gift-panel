import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const expectedRoutes = Object.freeze([
  ['config', '/?mode=config', 'text/html'],
  ['display', '/?mode=display', 'text/html'],
  ['api-config', '/api/config', 'application/json'],
]);
const panelName = 'bilibili-live-gift-panel';
const testVersion = '0.0.0';
const takeoverVersion = '0.0.1';

const delay = (milliseconds) => new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds));

async function fetchBeforeDeadline(fetchImpl, input, init, deadline, consume = (response) => response) {
  const remaining = deadline - Date.now();
  if (remaining <= 0) throw new Error('Windows EXE smoke request timed out');
  const controller = new AbortController();
  let timeout;
  try {
    const timedOut = new Promise((_, reject) => {
      timeout = setTimeout(() => {
        controller.abort();
        reject(new Error('Windows EXE smoke request timed out'));
      }, remaining);
    });
    const request = Promise.resolve(fetchImpl(input, { ...init, signal: controller.signal })).then(consume);
    return await Promise.race([request, timedOut]);
  } finally {
    clearTimeout(timeout);
  }
}

export async function probePanel(fetchImpl, ports, deadline) {
  let lastFailure = 'unavailable';
  while (Date.now() < deadline) {
    for (const port of ports) {
      try {
        const healthResponse = await fetchBeforeDeadline(fetchImpl, 'http://127.0.0.1:' + port + '/health', { redirect: 'error' }, deadline, async (response) => ({ response, health: response.status === 200 ? await response.json() : undefined }));
        if (healthResponse.response.status !== 200) {
          lastFailure = 'status ' + healthResponse.response.status;
          continue;
        }
        const health = healthResponse.health;
        if (health?.name !== panelName) throw new Error('Windows EXE smoke health endpoint belongs to a foreign panel');
        if (typeof health.version !== 'string' || !/^\d+\.\d+\.\d+$/.test(health.version)) {
          throw new Error('Windows EXE smoke health endpoint returned an invalid stable version');
        }
        return { port, version: health.version };
      } catch (error) {
        if (error instanceof Error && error.message.includes('foreign panel')) throw error;
        lastFailure = error instanceof Error ? error.message : String(error);
      }
    }
    await delay(Math.min(100, Math.max(0, deadline - Date.now())));
  }
  throw new Error('Windows EXE smoke readiness timed out: ' + lastFailure);
}

export async function requestGracefulExit(fetchImpl, port, requestedTakeoverVersion, deadline = Date.now() + 10_000) {
  const response = await fetchBeforeDeadline(fetchImpl, 'http://127.0.0.1:' + port + '/api/instance/exit', {
    method: 'POST',
    headers: { 'X-Bilibili-Panel-Takeover': requestedTakeoverVersion },
  }, deadline);
  if (response.status !== 202) throw new Error('Windows EXE smoke exit failed with status ' + response.status);
}

export async function validatePanelRoutes(fetchImpl, port, deadline = Date.now() + 10_000) {
  const passed = [];
  for (const [name, path, contentType] of expectedRoutes) {
    await fetchBeforeDeadline(fetchImpl, 'http://127.0.0.1:' + port + path, { redirect: 'error' }, deadline, async (response) => {
      const emptyFirstRunConfig = name === 'api-config' && response.status === 204;
      const typedSuccess = response.status === 200
        && response.headers.get('content-type')?.toLowerCase().startsWith(contentType);
      if (!emptyFirstRunConfig && !typedSuccess) {
        throw new Error('Windows EXE smoke route ' + name + ' failed');
      }
      await response.arrayBuffer();
    });
    passed.push(name);
  }
  return passed;
}

function childExited(child) {
  return child.exitCode !== null && child.exitCode !== undefined;
}

async function waitForChildExit(child, deadline) {
  while (!childExited(child) && Date.now() < deadline) await delay(Math.min(50, Math.max(0, deadline - Date.now())));
  if (!childExited(child)) throw new Error('Windows EXE smoke child did not exit within timeout');
}

async function cleanUpChild(child, deadline) {
  if (!child || childExited(child)) return true;
  try {
    child.kill();
  } catch {
    // Cleanup must continue even when the process handle is already invalid.
  }
  await waitForChildExit(child, deadline).catch(() => undefined);
  return childExited(child);
}

export async function smokeWindowsExecutable(options = {}) {
  const platform = options.platform ?? process.platform;
  if (platform !== 'win32') throw new Error('Windows EXE smoke can only run on Windows');

  const cwd = resolve(options.cwd ?? process.cwd());
  const executablePath = resolve(options.executablePath ?? join(cwd, 'dist', 'gift-panel.exe'));
  const evidencePath = resolve(cwd, 'dist', 'ci-smoke-evidence.json');
  const fetchImpl = options.fetchImpl ?? fetch;
  const spawnImpl = options.spawnImpl ?? spawn;
  const createTemporaryDirectory = options.createTemporaryDirectory ?? (() => mkdtemp(join(tmpdir(), 'bilibili-panel-smoke-')));
  const readExecutable = options.readExecutable ?? readFile;
  const readinessTimeoutMs = options.readinessTimeoutMs ?? 30_000;
  const exitTimeoutMs = options.exitTimeoutMs ?? 10_000;
  const pollIntervalMs = options.pollIntervalMs ?? 100;
  const requestTimeoutMs = options.requestTimeoutMs ?? 10_000;
  const removeTemporaryDirectory = options.removeTemporaryDirectory ?? ((path) => rm(path, { recursive: true, force: true }));
  const startedAt = new Date().toISOString();
  const temporaryAppData = await createTemporaryDirectory();
  let child;
  let removeChildErrorListener;

  try {
    const installedMarkerDirectory = join(temporaryAppData, 'BilibiliLiveGiftPanel', 'updates');
    await mkdir(installedMarkerDirectory, { recursive: true });
    await writeFile(
      join(installedMarkerDirectory, 'installed-update.json'),
      '{"version":"0.0.0"}\n',
      { encoding: 'utf8', flag: 'wx' },
    );
    child = spawnImpl(executablePath, [], {
      cwd,
      windowsHide: true,
      env: {
        ...process.env,
        APPDATA: temporaryAppData,
        GIFT_PANEL_CI_SMOKE: 'true',
        LOCALAPPDATA: temporaryAppData,
      },
    });
    let rejectChildError;
    const childFailure = new Promise((_, reject) => { rejectChildError = reject; });
    const onChildError = (error) => rejectChildError(new Error('Windows EXE smoke child failed: ' + (error instanceof Error ? error.message : String(error))));
    child.once?.('error', onChildError);
    removeChildErrorListener = () => child.removeListener?.('error', onChildError);
    const withChildFailure = (operation) => Promise.race([operation, childFailure]);
    const deadline = Date.now() + readinessTimeoutMs;
    let probe;
    while (true) {
      if (childExited(child)) throw new Error('Windows EXE smoke child exited before readiness');
      try {
        probe = await withChildFailure(probePanel(fetchImpl, ports(), Math.min(deadline, Date.now() + pollIntervalMs)));
        break;
      } catch (error) {
        if (error instanceof Error && error.message.includes('foreign panel')) throw error;
        if (error instanceof Error && error.message.startsWith('Windows EXE smoke child failed:')) throw error;
        if (Date.now() >= deadline) throw error;
      }
    }
    if (probe.version !== testVersion) throw new Error('Windows EXE smoke expected version ' + testVersion + ' but received ' + probe.version);
    const routes = await withChildFailure(validatePanelRoutes(fetchImpl, probe.port, Date.now() + requestTimeoutMs));
    const sha256 = createHash('sha256').update(await readExecutable(executablePath)).digest('hex');
    await withChildFailure(requestGracefulExit(fetchImpl, probe.port, takeoverVersion, Date.now() + requestTimeoutMs));
    await withChildFailure(waitForChildExit(child, Date.now() + exitTimeoutMs));
    const evidence = { schema: 1, version: probe.version, port: probe.port, routes, sha256, startedAt, completedAt: new Date().toISOString() };
    await mkdir(dirname(evidencePath), { recursive: true });
    await writeFile(evidencePath, JSON.stringify(evidence) + '\n', 'utf8');
    return evidence;
  } finally {
    let childStopped = true;
    try {
      childStopped = await cleanUpChild(child, Date.now() + exitTimeoutMs);
    } finally {
      await removeTemporaryDirectory(temporaryAppData);
      if (childStopped) removeChildErrorListener?.();
    }
  }
}

function ports() {
  return Array.from({ length: 10 }, (_, index) => 12450 + index);
}

const isMain = process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url;
if (isMain) {
  smokeWindowsExecutable().then(
    (evidence) => console.log(JSON.stringify(evidence)),
    (error) => { console.error(error instanceof Error ? error.message : String(error)); process.exitCode = 1; },
  );
}
