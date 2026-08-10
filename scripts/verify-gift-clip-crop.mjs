import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');

const baseURL = process.env.GIFT_CLIP_UI_URL ?? 'http://127.0.0.1:12462/tests/fixtures/gift-receipts.html';
const artifactDir = resolve(process.cwd(), 'artifacts');
await mkdir(artifactDir, { recursive: true });

async function dragBy(page, locator, deltaX, deltaY) {
  const bounds = await locator.boundingBox();
  assert.ok(bounds, 'drag target must have a bounding box');
  const startX = bounds.x + (bounds.width / 2);
  const startY = bounds.y + (bounds.height / 2);
  await page.mouse.move(startX, startY);
  await page.mouse.down();
  await page.mouse.move(startX + deltaX, startY + deltaY, { steps: 6 });
  await page.mouse.up();
}

async function waitForCropStatus(page, dimensions) {
  await page.waitForFunction((expected) => (
    document.querySelector('.gift-clip-status')?.textContent?.includes(`剪裁 ${expected}`)
  ), dimensions);
  const text = await page.locator('.gift-clip-status').textContent();
  assert.ok(text?.includes(`剪裁 ${dimensions}`), `unexpected crop status: ${text}`);
  return text ?? '';
}

async function openValidAnimation(page) {
  await page.getByRole('button', { name: '制作回放' }).first().click();
  await page.locator('.gift-clip-crop-frame').waitFor();
}

async function closeStudio(page) {
  const dialog = page.getByRole('dialog', { name: '制作礼物动画回放' });
  await dialog.getByRole('button', { name: '关闭', exact: true }).click();
  await dialog.waitFor({ state: 'detached' });
}

async function readSavedCrop(page) {
  return page.evaluate(() => {
    const crops = window.__giftReceiptFixtureState?.settings?.giftClipCrops ?? {};
    return {
      count: Object.keys(crops).length,
      crop: Object.values(crops)[0] ?? null,
    };
  });
}

function assertSavedCrop(saved) {
  assert.equal(saved.count, 1, `expected one persisted crop, received ${saved.count}`);
  assert.ok(saved.crop, 'persisted crop must be present');
  const { x, y, width, height } = saved.crop;
  assert.ok([x, y, width, height].every(Number.isFinite), 'persisted crop coordinates must be finite');
  assert.ok(x >= 0 && y >= 0 && width > 0 && height > 0, 'persisted crop must have positive in-bounds dimensions');
  assert.ok(x + width <= 1 + Number.EPSILON && y + height <= 1 + Number.EPSILON, 'persisted crop must stay normalized');
  assert.ok(Math.abs(x - 0.2) < 1e-12 && Math.abs(y) < 1e-12, `unexpected persisted crop origin: ${x}, ${y}`);
  const pixels = {
    x: Math.round(x * 640),
    y: Math.round(y * 360),
    width: Math.round(width * 640),
    height: Math.round(height * 360),
  };
  assert.deepEqual(pixels, { x: 128, y: 0, width: 512, height: 360 });
  return pixels;
}

const browser = await chromium.launch({ headless: true });
let summary;
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1 });
  const errors = [];
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(`console: ${message.text()}`);
  });
  page.on('pageerror', (error) => errors.push(`page: ${error.message}`));

  await page.goto(baseURL);
  await page.locator('.config-nav-button[data-config-page="data"]').click();
  await openValidAnimation(page);
  await page.screenshot({ path: resolve(artifactDir, 'gift-clip-crop-desktop.png') });

  const handleCount = await page.locator('.gift-clip-crop-handle').count();
  assert.equal(handleCount, 8, `expected eight crop handles, received ${handleCount}`);
  await waitForCropStatus(page, '640 × 360');

  const stage = page.locator('.gift-clip-stage');
  const stageBounds = await stage.boundingBox();
  assert.ok(stageBounds, 'crop stage must have a bounding box');
  assert.ok(Math.abs(stageBounds.width - 480) <= 0.5, `expected a 480px crop stage, received ${stageBounds.width}px`);

  await dragBy(page, page.locator('.gift-clip-crop-handle.is-w'), 96, 0);
  await waitForCropStatus(page, '512 × 360');

  const frame = page.locator('.gift-clip-crop-frame');
  const beforeMove = await frame.boundingBox();
  assert.ok(beforeMove, 'crop frame must have a bounding box before movement');
  await dragBy(page, frame, stageBounds.width + 200, stageBounds.height + 200);
  const movedFrame = await frame.boundingBox();
  const movedStage = await stage.boundingBox();
  assert.ok(movedFrame && movedStage, 'crop frame and stage must remain measurable after movement');
  const edgeTolerance = 1;
  assert.ok(movedFrame.x >= movedStage.x - edgeTolerance, `crop frame escaped left edge: ${movedFrame.x - movedStage.x}px`);
  assert.ok(movedFrame.y >= movedStage.y - edgeTolerance, `crop frame escaped top edge: ${movedFrame.y - movedStage.y}px`);
  assert.ok(
    movedFrame.x + movedFrame.width <= movedStage.x + movedStage.width + edgeTolerance,
    `crop frame escaped right edge: ${(movedFrame.x + movedFrame.width) - (movedStage.x + movedStage.width)}px`,
  );
  assert.ok(
    movedFrame.y + movedFrame.height <= movedStage.y + movedStage.height + edgeTolerance,
    `crop frame escaped bottom edge: ${(movedFrame.y + movedFrame.height) - (movedStage.y + movedStage.height)}px`,
  );

  await page.getByRole('button', { name: '确定剪裁并生成' }).click();
  await page.waitForFunction(() => (
    Object.keys(window.__giftReceiptFixtureState?.settings?.giftClipCrops ?? {}).length === 1
  ));
  const saved = await readSavedCrop(page);
  const savedPixels = assertSavedCrop(saved);

  const preview = page.locator('.gift-clip-video');
  await preview.waitFor({ state: 'visible', timeout: 30_000 });
  await page.waitForFunction(() => {
    const video = document.querySelector('.gift-clip-video');
    return video instanceof HTMLVideoElement && video.videoWidth > 0 && video.videoHeight > 0;
  });
  const videoDimensions = await preview.evaluate((video) => ({
    width: video.videoWidth,
    height: video.videoHeight,
  }));
  assert.deepEqual(videoDimensions, { width: 512, height: 360 });
  await page.screenshot({ path: resolve(artifactDir, 'gift-clip-crop-preview.png') });

  await closeStudio(page);
  await openValidAnimation(page);
  await waitForCropStatus(page, '512 × 360');

  await dragBy(page, page.locator('.gift-clip-crop-handle.is-e'), -48, 0);
  await waitForCropStatus(page, '448 × 360');
  await closeStudio(page);
  await openValidAnimation(page);
  await waitForCropStatus(page, '512 × 360');

  await page.getByRole('button', { name: '恢复完整画面' }).click();
  await waitForCropStatus(page, '640 × 360');
  await closeStudio(page);
  assertSavedCrop(await readSavedCrop(page));

  const tinyRow = page.locator('.gift-history-row').filter({ hasText: '微型动画' });
  assert.equal(await tinyRow.count(), 1, 'expected one tiny-animation receipt row');
  await tinyRow.getByRole('button', { name: '制作回放' }).click();
  const tinyDialog = page.getByRole('dialog', { name: '制作礼物动画回放' });
  await tinyDialog.getByText('动画尺寸过小，无法制作回放（63 × 120）', { exact: true }).waitFor();
  assert.equal(
    await tinyDialog.getByRole('button', { name: '确定剪裁并生成' }).count(),
    0,
    'tiny animation must not expose the confirm action',
  );
  assert.equal(await tinyDialog.locator('.gift-clip-crop-frame').count(), 0, 'tiny animation must not create a crop frame');
  await closeStudio(page);

  await page.setViewportSize({ width: 390, height: 844 });
  await openValidAnimation(page);
  await waitForCropStatus(page, '512 × 360');
  await page.screenshot({ path: resolve(artifactDir, 'gift-clip-crop-mobile.png') });
  const overflow = await page.evaluate(() => {
    const dialog = document.querySelector('.gift-clip-dialog');
    if (!(dialog instanceof HTMLElement)) throw new Error('gift clip dialog missing');
    return {
      document: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      dialog: dialog.scrollWidth - dialog.clientWidth,
    };
  });
  assert.ok(overflow.document <= 1, `document horizontal overflow is ${overflow.document}px`);
  assert.ok(overflow.dialog <= 1, `dialog horizontal overflow is ${overflow.dialog}px`);

  await page.waitForTimeout(100);
  assert.equal(errors.length, 0, `Browser errors:\n${errors.join('\n')}`);
  summary = {
    handles: handleCount,
    savedCrop: `${savedPixels.width} × ${savedPixels.height}`,
    video: `${videoDimensions.width} × ${videoDimensions.height}`,
    screenshots: 3,
    overflow,
    consoleErrors: errors.length,
  };
} finally {
  await browser.close();
}

console.log(`handles: ${summary.handles}`);
console.log(`savedCrop: ${summary.savedCrop}`);
console.log(`video: ${summary.video}`);
console.log(`screenshots: ${summary.screenshots}`);
console.log(`overflow: document=${summary.overflow.document}px dialog=${summary.overflow.dialog}px`);
console.log(`consoleErrors: ${summary.consoleErrors}`);
