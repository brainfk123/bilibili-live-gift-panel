import { spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

if (process.platform === 'darwin') {
  const projectRoot = fileURLToPath(new URL('..', import.meta.url));
  const commandOptions = {
    cwd: projectRoot,
    encoding: 'utf8' as const,
    maxBuffer: 64 * 1024 * 1024,
    timeout: 240_000,
    killSignal: 'SIGKILL' as const,
  };

  describe('hosted deployment Linux contract', () => {
    it('passes in the pinned Linux contract-test image', () => {
      const temporaryDirectory = mkdtempSync(join(tmpdir(), 'hosted-deploy-contract-'));
      const imageIdFile = join(temporaryDirectory, 'image-id');
      const containerName = `hosted-deploy-contract-${randomUUID()}`;
      try {
        const build = spawnSync('docker', [
          'build',
          '--file', 'tests/Dockerfile.hosted-deploy',
          '--iidfile', imageIdFile,
          '.',
        ], commandOptions);
        expect(
          build.status,
          build.error?.message ?? `${build.stdout}\n${build.stderr}`,
        ).toBe(0);

        const imageId = readFileSync(imageIdFile, 'utf8').trim();
        expect(imageId).toMatch(/^sha256:[a-f0-9]{64}$/);
        const run = spawnSync('docker', ['run', '--rm', '--name', containerName, imageId], commandOptions);
        expect(
          run.status,
          run.error?.message ?? `${run.stdout}\n${run.stderr}`,
        ).toBe(0);
      } finally {
        // Killing the CLI does not stop a container already running in the daemon.
        spawnSync('docker', ['rm', '--force', containerName], { ...commandOptions, timeout: 10_000 });
        rmSync(temporaryDirectory, { recursive: true, force: true });
      }
    }, 600_000);
  });
} else {
  await import('./hosted-deploy.contract');
}
