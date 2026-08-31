import { createServer } from 'node:http';
import { EventEmitter } from 'node:events';
import { mkdir, mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, it } from 'vitest';
import { probePanel, requestGracefulExit, smokeWindowsExecutable, validatePanelRoutes } from '../scripts/smoke-windows-exe.mjs';

function within<T>(promise: Promise<T>, milliseconds = 100): Promise<T> {
  return Promise.race([promise, new Promise<T>((_resolve, reject) => setTimeout(() => reject(new Error('test deadline exceeded')), milliseconds))]);
}

it('probes pages, config API, and graceful takeover exit', async () => {
  let exited = false;
  const server = createServer((request, response) => {
    if (request.url === '/health') return void response.end('{"name":"bilibili-live-gift-panel","version":"0.0.0"}');
    if (request.url === '/api/instance/exit') {
      exited = request.headers['x-bilibili-panel-takeover'] === '0.0.1';
      response.statusCode = 202;
      return void response.end('{"code":0}');
    }
    if (request.url === '/api/config') {
      response.setHeader('content-type', 'application/json');
      return void response.end('{"schemaVersion":12}');
    }
    response.setHeader('content-type', 'text/html');
    response.end('<!doctype html><title>panel</title>');
  });
  await new Promise<void>((resolveListening) => server.listen(0, '127.0.0.1', resolveListening));
  const port = (server.address() as { port: number }).port;
  try {
    expect(await probePanel(fetch, [port], Date.now() + 2_000)).toMatchObject({ port, version: '0.0.0' });
    expect(await validatePanelRoutes(fetch, port)).toEqual(['config', 'display', 'api-config']);
    await requestGracefulExit(fetch, port, '0.0.1');
    expect(exited).toBe(true);
  } finally {
    server.close();
  }
});

it('rejects a health response without the panel marker', async () => {
  const foreignFetch: typeof fetch = async () => new Response('{"name":"foreign","version":"0.0.0"}');
  await expect(probePanel(foreignFetch, [12450], Date.now() + 50)).rejects.toThrow('foreign');
});

it('rejects an unexpected route status and exit status', async () => {
  const fetchWithBrokenRoute: typeof fetch = async () => new Response('', { status: 500, headers: { 'content-type': 'text/html' } });
  const fetchWithRejectedExit: typeof fetch = async () => new Response('', { status: 200 });
  await expect(validatePanelRoutes(fetchWithBrokenRoute, 12450)).rejects.toThrow('config');
  await expect(requestGracefulExit(fetchWithRejectedExit, 12450, '0.0.1')).rejects.toThrow('200');
});

it('times out instead of accepting an unavailable port', async () => {
  const unavailableFetch: typeof fetch = async () => { throw new Error('connect ECONNREFUSED'); };
  await expect(probePanel(unavailableFetch, [12450], Date.now() + 20)).rejects.toThrow('timed out');
});

it('aborts never-settling health, route, and takeover requests at their deadlines', async () => {
  const neverFetch: typeof fetch = () => new Promise<Response>(() => undefined);
  await expect(within(probePanel(neverFetch, [12450], Date.now() + 20))).rejects.toThrow('request timed out');
  await expect(within(validatePanelRoutes(neverFetch, 12450, Date.now() + 20))).rejects.toThrow('request timed out');
  await expect(within(requestGracefulExit(neverFetch, 12450, '0.0.1', Date.now() + 20))).rejects.toThrow('request timed out');
});

it('bounds a health body that stalls after headers and still cleans up', async () => {
  const root = await mkdtemp(join(tmpdir(), 'windows-smoke-stalled-health-body-'));
  let aborted = false;
  let removed = false;
  const child = { exitCode: null as number | null, kill: () => { child.exitCode = 1; return true; }, once: () => undefined };
  const stalledHealth = {
    status: 200,
    headers: new Headers({ 'content-type': 'application/json' }),
    json: () => new Promise(() => undefined),
  } as unknown as Response;
  try {
    await expect(within(smokeWindowsExecutable({
      platform: 'win32', cwd: root, executablePath: join(root, 'gift-panel.exe'),
      createTemporaryDirectory: async () => { const path = join(root, 'LOCALAPPDATA'); await mkdir(path); return path; },
      removeTemporaryDirectory: async () => { removed = true; },
      spawnImpl: () => child,
      fetchImpl: async (_input, init) => { init?.signal?.addEventListener('abort', () => { aborted = true; }); return stalledHealth; },
      readinessTimeoutMs: 20, exitTimeoutMs: 1, pollIntervalMs: 1,
    }))).rejects.toThrow('request timed out');
    expect(aborted).toBe(true);
    expect(removed).toBe(true);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

it('consumes a child error emitted after cleanup times out', async () => {
  const root = await mkdtemp(join(tmpdir(), 'windows-smoke-late-child-error-'));
  const child = Object.assign(new EventEmitter(), { exitCode: null as number | null, kill: () => {
    setTimeout(() => child.emit('error', new Error('late synthetic error')), 10);
    return true;
  } });
  try {
    await expect(smokeWindowsExecutable({
      platform: 'win32', cwd: root, executablePath: join(root, 'gift-panel.exe'),
      createTemporaryDirectory: async () => { const path = join(root, 'LOCALAPPDATA'); await mkdir(path); return path; },
      spawnImpl: () => child,
      fetchImpl: async () => new Response('{"name":"bilibili-live-gift-panel","version":"0.0.1"}'),
      readinessTimeoutMs: 50, exitTimeoutMs: 1, pollIntervalMs: 1,
    })).rejects.toThrow('expected version 0.0.0');
    await new Promise<void>((resolveWait) => setTimeout(resolveWait, 20));
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

it('turns an emitted spawn error into a controlled smoke failure', async () => {
  const root = await mkdtemp(join(tmpdir(), 'windows-smoke-spawn-error-'));
  const child = Object.assign(new EventEmitter(), { exitCode: null as number | null, kill: () => true });
  try {
    await expect(smokeWindowsExecutable({
      platform: 'win32', cwd: root, executablePath: join(root, 'gift-panel.exe'),
      createTemporaryDirectory: async () => { const path = join(root, 'LOCALAPPDATA'); await mkdir(path); return path; },
      spawnImpl: () => { setTimeout(() => child.emit('error', new Error('synthetic spawn error')), 1); return child; },
      fetchImpl: () => new Promise<Response>(() => undefined),
      readinessTimeoutMs: 50, exitTimeoutMs: 1, requestTimeoutMs: 50,
    })).rejects.toThrow('child failed: synthetic spawn error');
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

it('waits for delayed forced cleanup before removing LOCALAPPDATA', async () => {
  const root = await mkdtemp(join(tmpdir(), 'windows-smoke-delayed-cleanup-'));
  const child = Object.assign(new EventEmitter(), { exitCode: null as number | null, kill: () => {
    setTimeout(() => { child.exitCode = 1; child.emit('close', 1); }, 5);
    return true;
  } });
  let closed = false;
  child.once('close', () => { closed = true; });
  try {
    await expect(smokeWindowsExecutable({
      platform: 'win32', cwd: root, executablePath: join(root, 'gift-panel.exe'),
      createTemporaryDirectory: async () => { const path = join(root, 'LOCALAPPDATA'); await mkdir(path); return path; },
      removeTemporaryDirectory: async () => expect(closed).toBe(true),
      spawnImpl: () => child,
      fetchImpl: async () => new Response('{"name":"bilibili-live-gift-panel","version":"0.0.1"}'),
      readinessTimeoutMs: 50, exitTimeoutMs: 50, pollIntervalMs: 1,
    })).rejects.toThrow('expected version 0.0.0');
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

it('removes LOCALAPPDATA when forced child termination fails', async () => {
  const root = await mkdtemp(join(tmpdir(), 'windows-smoke-failed-cleanup-'));
  let removed = false;
  try {
    await expect(smokeWindowsExecutable({
      platform: 'win32', cwd: root, executablePath: join(root, 'gift-panel.exe'),
      createTemporaryDirectory: async () => { const path = join(root, 'LOCALAPPDATA'); await mkdir(path); return path; },
      removeTemporaryDirectory: async () => { removed = true; },
      spawnImpl: () => ({ exitCode: null, kill: () => { throw new Error('synthetic kill failure'); }, once: () => undefined }),
      fetchImpl: async () => new Response('{"name":"bilibili-live-gift-panel","version":"0.0.1"}'),
      readinessTimeoutMs: 50, exitTimeoutMs: 1, pollIntervalMs: 1,
    })).rejects.toThrow('expected version 0.0.0');
    expect(removed).toBe(true);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

it('rejects a child that exits before readiness', async () => {
  const root = await mkdtemp(join(tmpdir(), 'windows-smoke-child-exit-'));
  try {
    await expect(smokeWindowsExecutable({
      platform: 'win32',
      cwd: root,
      executablePath: join(root, 'gift-panel.exe'),
      createTemporaryDirectory: async () => { const path = join(root, 'LOCALAPPDATA'); await mkdir(path); return path; },
      spawnImpl: () => ({ exitCode: 1, kill: () => true, once: () => undefined }),
      fetchImpl: async () => { throw new Error('connect ECONNREFUSED'); },
      readinessTimeoutMs: 50,
      pollIntervalMs: 1,
    })).rejects.toThrow('exited before readiness');
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

it('rejects a ready panel built with a version other than 0.0.0', async () => {
  const root = await mkdtemp(join(tmpdir(), 'windows-smoke-version-'));
  try {
    await expect(smokeWindowsExecutable({
      platform: 'win32', cwd: root, executablePath: join(root, 'gift-panel.exe'),
      createTemporaryDirectory: async () => { const path = join(root, 'LOCALAPPDATA'); await mkdir(path); return path; },
      spawnImpl: () => ({ exitCode: null, kill: () => true }),
      fetchImpl: async () => new Response('{"name":"bilibili-live-gift-panel","version":"0.0.1"}'),
      readinessTimeoutMs: 50, exitTimeoutMs: 1, pollIntervalMs: 1,
    })).rejects.toThrow('expected version 0.0.0');
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

it('writes allowlisted evidence only', async () => {
  const root = await mkdtemp(join(tmpdir(), 'windows-smoke-evidence-'));
  const evidencePath = join(root, 'dist', 'ci-smoke-evidence.json');
  const child = { exitCode: null as number | null, kill: () => { child.exitCode = 0; return true; }, once: () => undefined };
  const responses = new Map<string, Response>([
    ['/health', new Response('{"name":"bilibili-live-gift-panel","version":"0.0.0","token":"secret"}')],
    ['/?mode=config', new Response('<html>cookie=secret</html>', { headers: { 'content-type': 'text/html' } })],
    ['/?mode=display', new Response('<html>nickname=secret</html>', { headers: { 'content-type': 'text/html' } })],
    ['/api/config', new Response('{"uid":1}', { headers: { 'content-type': 'application/json' } })],
    ['/api/instance/exit', new Response('', { status: 202 })],
  ]);
  try {
    const evidence = await smokeWindowsExecutable({
      platform: 'win32', cwd: root, executablePath: join(root, 'gift-panel.exe'),
      createTemporaryDirectory: async () => { const path = join(root, 'LOCALAPPDATA'); await mkdir(path); return path; }, spawnImpl: () => child,
      fetchImpl: async (input) => {
        const path = new URL(String(input)).pathname + new URL(String(input)).search;
        if (path === '/api/instance/exit') child.exitCode = 0;
        return responses.get(path)!;
      },
      readinessTimeoutMs: 50, exitTimeoutMs: 50, pollIntervalMs: 1,
      readExecutable: async () => Buffer.from('fake executable'),
    });
    expect(evidence).toEqual(expect.objectContaining({ schema: 1, version: '0.0.0', port: 12450, routes: ['config', 'display', 'api-config'] }));
    const contents = await readFile(evidencePath, 'utf8');
    for (const forbidden of ['cookie', 'token', 'uid', 'nickname', 'LOCALAPPDATA', '<html>', 'secret']) expect(contents.toLowerCase()).not.toContain(forbidden.toLowerCase());
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
