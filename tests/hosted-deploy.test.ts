import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

if (process.platform === 'darwin') {
  const projectRoot = fileURLToPath(new URL('..', import.meta.url));
  const imageTag = 'bilibili-live-gift-panel-hosted-deploy-contract:local';
  const commandOptions = {
    cwd: projectRoot,
    encoding: 'utf8' as const,
    maxBuffer: 64 * 1024 * 1024,
  };

  describe('hosted deployment Linux contract', () => {
    it('passes in the pinned Linux contract-test image', () => {
      const build = spawnSync('docker', [
        'build',
        '--file', 'tests/Dockerfile.hosted-deploy',
        '--tag', imageTag,
        '.',
      ], commandOptions);
      expect(
        build.status,
        build.error?.message ?? `${build.stdout}\n${build.stderr}`,
      ).toBe(0);

      const run = spawnSync('docker', ['run', '--rm', imageTag], commandOptions);
      expect(
        run.status,
        run.error?.message ?? `${run.stdout}\n${run.stderr}`,
      ).toBe(0);
    }, 600_000);
  });
} else {
  await import('./hosted-deploy.contract');
}
