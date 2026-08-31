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

export async function probePanel(fetchImpl, ports, deadline) {
  let lastFailure = 'unavailable';
  while (Date.now() < deadline) {
    for (const port of ports) {
      try {
        const response = await fetchImpl('http://127.0.0.1:' + port + '/health', { redirect: 'error' });
        if (response.status !== 200) {
          lastFailure = 'status ' + response.status;
          continue;
        }
        const health = await response.json();
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

export async function requestGracefulExit(fetchImpl, port, requestedTakeoverVersion) {
  const response = await fetchImpl('http://127.0.0.1:' + port + '/api/instance/exit', {
    method: 'POST',
    headers: { 'X-Bilibili-Panel-Takeover': requestedTakeoverVersion },
  });
  if (response.status !== 202) throw new Error('Windows EXE smoke exit failed with status ' + response.status);
}

export async function validatePanelRoutes(fetchImpl, port) {
  const passed = [];
  for (const [name, path, contentType] of expectedRoutes) {
    const response = await fetchImpl('http://127.0.0.1:' + port + path, { redirect: 'error' });
    if (response.status !== 200 || !response.headers.get('content-type')?.toLowerCase().startsWith(contentType)) {
      throw new Error('Windows EXE smoke route ' + name + ' failed');
    }
    await response.arrayBuffer();
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
  const startedAt = new Date().toISOString();
  const temporaryLocalAppData = await createTemporaryDirectory();
  let child;

  try {
    child = spawnImpl(executablePath, [], {
      cwd,
      windowsHide: true,
      env: { ...process.env, LOCALAPPDATA: temporaryLocalAppData },
    });
    const deadline = Date.now() + readinessTimeoutMs;
    let probe;
    while (true) {
      if (childExited(child)) throw new Error('Windows EXE smoke child exited before readiness');
      try {
        probe = await probePanel(fetchImpl, ports(), Math.min(deadline, Date.now() + pollIntervalMs));
        break;
      } catch (error) {
        if (error instanceof Error && error.message.includes('foreign panel')) throw error;
        if (Date.now() >= deadline) throw error;
      }
    }
    if (probe.version !== testVersion) throw new Error('Windows EXE smoke expected version ' + testVersion + ' but received ' + probe.version);
    const routes = await validatePanelRoutes(fetchImpl, probe.port);
    const sha256 = createHash('sha256').update(await readExecutable(executablePath)).digest('hex');
    await requestGracefulExit(fetchImpl, probe.port, takeoverVersion);
    await waitForChildExit(child, Date.now() + exitTimeoutMs);
    const evidence = { schema: 1, version: probe.version, port: probe.port, routes, sha256, startedAt, completedAt: new Date().toISOString() };
    await mkdir(dirname(evidencePath), { recursive: true });
    await writeFile(evidencePath, JSON.stringify(evidence) + '\n', 'utf8');
    return evidence;
  } finally {
    if (child && !childExited(child)) child.kill();
    await rm(temporaryLocalAppData, { recursive: true, force: true });
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
