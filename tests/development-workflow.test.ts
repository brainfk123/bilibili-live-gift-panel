import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';

it('documents Hosted-first Mac development without claiming ARM is x64 acceptance', () => {
  const text = readFileSync(new URL('../docs/development/mac-hosted-workflow.md', import.meta.url), 'utf8');
  for (const command of ['npm ci', 'npm test', 'npm run typecheck', 'npm run build:hosted', 'go -C goserver test ./...']) {
    expect(text).toContain(command);
  }
  expect(text).toContain('交叉编译不等于 Windows 验收');
  expect(text).toContain('Windows 11 ARM');
  expect(text).toContain('不作为 x64 驱动、显卡或硬件编码证据');
  expect(text).toContain('5a0bbfb');
});
