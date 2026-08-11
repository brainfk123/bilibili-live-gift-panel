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

async function resizeCropToMinimum(page) {
  async function shrink(handle, key, presses) {
    await page.locator(`.gift-clip-crop-handle.is-${handle}`).focus();
    await page.keyboard.down('Shift');
    try {
      for (let index = 0; index < presses; index += 1) {
        await page.keyboard.press(key);
      }
    } finally {
      await page.keyboard.up('Shift');
    }
  }

  await shrink('e', 'ArrowLeft', 64);
  await shrink('s', 'ArrowUp', 36);
  await waitForCropStatus(page, '64 × 64');
}

async function inspectMinimumCropTargetability(page, layout) {
  const contract = await page.evaluate(() => {
    const frame = document.querySelector('.gift-clip-crop-frame');
    if (!(frame instanceof HTMLElement)) throw new Error('.gift-clip-crop-frame missing');
    const frameBounds = frame.getBoundingClientRect();
    const handles = [...frame.querySelectorAll('.gift-clip-crop-handle')].map((element) => {
      if (!(element instanceof HTMLElement)) throw new Error('crop handle must be an HTMLElement');
      const bounds = element.getBoundingClientRect();
      const center = {
        x: bounds.left + bounds.width / 2,
        y: bounds.top + bounds.height / 2,
      };
      const hit = document.elementFromPoint(center.x, center.y);
      return {
        handle: element.dataset.handle,
        hitHandle: hit instanceof HTMLElement ? hit.dataset.handle ?? null : null,
        bounds: {
          left: bounds.left,
          top: bounds.top,
          right: bounds.right,
          bottom: bounds.bottom,
          width: bounds.width,
          height: bounds.height,
        },
      };
    });
    const overlaps = [];
    for (let leftIndex = 0; leftIndex < handles.length; leftIndex += 1) {
      for (let rightIndex = leftIndex + 1; rightIndex < handles.length; rightIndex += 1) {
        const left = handles[leftIndex];
        const right = handles[rightIndex];
        const overlapWidth = Math.min(left.bounds.right, right.bounds.right)
          - Math.max(left.bounds.left, right.bounds.left);
        const overlapHeight = Math.min(left.bounds.bottom, right.bounds.bottom)
          - Math.max(left.bounds.top, right.bounds.top);
        if (overlapWidth > 0.5 && overlapHeight > 0.5) {
          overlaps.push(`${left.handle}/${right.handle}`);
        }
      }
    }
    const frameCenterHit = document.elementFromPoint(
      frameBounds.left + frameBounds.width / 2,
      frameBounds.top + frameBounds.height / 2,
    );
    return {
      frame: { width: frameBounds.width, height: frameBounds.height },
      handles,
      overlaps,
      frameCenterIsGrabSurface: frameCenterHit === frame,
    };
  });

  return {
    layout,
    ...contract,
    failures: [
      ...contract.handles
        .filter(({ handle, hitHandle }) => handle !== hitHandle)
        .map(({ handle, hitHandle }) => `${layout} ${handle} center hits ${hitHandle ?? 'non-handle'}`),
      ...contract.overlaps.map((pair) => `${layout} overlapping handles ${pair}`),
      ...(contract.frameCenterIsGrabSurface ? [] : [`${layout} frame center is not a grab surface`]),
    ],
  };
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

  const handleCount = await page.locator('.gift-clip-crop-handle').count();
  assert.equal(handleCount, 8, `expected eight crop handles, received ${handleCount}`);
  await waitForCropStatus(page, '640 × 360');

  const stage = page.locator('.gift-clip-stage');
  const stageBounds = await stage.boundingBox();
  assert.ok(stageBounds, 'crop stage must have a bounding box');
  assert.ok(Math.abs(stageBounds.width - 480) <= 0.5, `expected a 480px crop stage, received ${stageBounds.width}px`);

  const frame = page.locator('.gift-clip-crop-frame');
  const visualContract = await page.evaluate(() => {
    const requireElement = (selector) => {
      const element = document.querySelector(selector);
      assertElement(element, selector);
      return element;
    };
    function assertElement(element, selector) {
      if (!(element instanceof HTMLElement)) throw new Error(`${selector} missing`);
    }
    const style = (selector, pseudo) => getComputedStyle(requireElement(selector), pseudo);
    return {
      stageRadius: style('.gift-clip-stage').borderRadius,
      viewportRadius: style('.gift-clip-crop-viewport').borderRadius,
      frameRadius: style('.gift-clip-crop-frame').borderRadius,
      frameCursor: style('.gift-clip-crop-frame').cursor,
      frameBoxShadow: style('.gift-clip-crop-frame').boxShadow,
      dialogRadius: style('.gift-clip-dialog').borderRadius,
      handleWidth: style('.gift-clip-crop-handle').width,
      handleHeight: style('.gift-clip-crop-handle').height,
      handleRadius: style('.gift-clip-crop-handle').borderRadius,
      cornerTop: style('.gift-clip-crop-handle.is-nw', '::before').borderTopWidth,
      cornerLeft: style('.gift-clip-crop-handle.is-nw', '::before').borderLeftWidth,
      edgeWidth: style('.gift-clip-crop-handle.is-n', '::before').width,
      edgeHeight: style('.gift-clip-crop-handle.is-n', '::before').height,
      guidesOpacity: style('.gift-clip-crop-guides').opacity,
    };
  });

  assert.equal(visualContract.stageRadius, '0px');
  assert.equal(visualContract.viewportRadius, '0px');
  assert.equal(visualContract.frameRadius, '0px');
  assert.equal(visualContract.frameCursor, 'grab');
  assert.ok(Number.parseFloat(visualContract.dialogRadius) > 0, 'desktop dialog must retain rounding');
  assert.equal(visualContract.handleWidth, '28px');
  assert.equal(visualContract.handleHeight, '28px');
  assert.equal(visualContract.handleRadius, '0px');
  assert.equal(visualContract.cornerTop, '2px');
  assert.equal(visualContract.cornerLeft, '2px');
  assert.equal(visualContract.edgeWidth, '14px');
  assert.equal(visualContract.edgeHeight, '3px');
  assert.equal(Number(visualContract.guidesOpacity), 0);

  const handleCursors = await page.locator('.gift-clip-crop-handle').evaluateAll((handles) => (
    Object.fromEntries(handles.map((handle) => [handle.dataset.handle, getComputedStyle(handle).cursor]))
  ));
  assert.deepEqual(handleCursors, {
    n: 'ns-resize', ne: 'nesw-resize', e: 'ew-resize', se: 'nwse-resize',
    s: 'ns-resize', sw: 'nesw-resize', w: 'ew-resize', nw: 'nwse-resize',
  });

  const frameBoundsForState = await frame.boundingBox();
  assert.ok(frameBoundsForState, 'frame must be measurable for state checks');
  await page.mouse.move(
    frameBoundsForState.x + frameBoundsForState.width / 2,
    frameBoundsForState.y + frameBoundsForState.height / 2,
  );
  await page.mouse.down();
  await page.waitForFunction(() => document.querySelector('.gift-clip-crop-frame')?.classList.contains('is-moving'));
  assert.equal(await frame.evaluate((element) => getComputedStyle(element).cursor), 'grabbing');
  await page.waitForFunction(() => Number(getComputedStyle(document.querySelector('.gift-clip-crop-guides')).opacity) > 0);
  const activeFrameContract = await frame.evaluate((element) => {
    const bounds = element.getBoundingClientRect();
    return {
      boxShadow: getComputedStyle(element).boxShadow,
      width: bounds.width,
      height: bounds.height,
    };
  });
  assert.notEqual(
    activeFrameContract.boxShadow,
    visualContract.frameBoxShadow,
    'adjustment must strengthen the frame accent without changing layout',
  );
  assert.ok(
    Math.abs(activeFrameContract.width - frameBoundsForState.width) <= 0.5
      && Math.abs(activeFrameContract.height - frameBoundsForState.height) <= 0.5,
    'adjustment accent must not change frame geometry',
  );
  await page.screenshot({ path: resolve(artifactDir, 'gift-clip-crop-desktop.png') });
  await page.mouse.up();
  await page.waitForFunction(() => Number(getComputedStyle(document.querySelector('.gift-clip-crop-guides')).opacity) === 0);

  await page.emulateMedia({ reducedMotion: 'reduce' });
  const reducedMotionContract = await page.evaluate(() => ({
    frame: getComputedStyle(document.querySelector('.gift-clip-crop-frame')).transitionDuration,
    guides: getComputedStyle(document.querySelector('.gift-clip-crop-guides')).transitionDuration,
    handle: getComputedStyle(document.querySelector('.gift-clip-crop-handle'), '::before').transitionDuration,
  }));
  assert.deepEqual(reducedMotionContract, { frame: '0s', guides: '0s', handle: '0s' });
  await page.emulateMedia({ reducedMotion: 'no-preference' });

  await frame.focus();
  await page.keyboard.down('ArrowLeft');
  await page.waitForFunction(() => document.querySelector('.gift-clip-crop-frame')?.classList.contains('is-adjusting'));
  await page.waitForFunction(() => Number(getComputedStyle(document.querySelector('.gift-clip-crop-guides')).opacity) > 0);
  await page.keyboard.up('ArrowLeft');
  await page.waitForFunction(() => !document.querySelector('.gift-clip-crop-frame')?.classList.contains('is-adjusting'));
  await page.waitForFunction(() => Number(getComputedStyle(document.querySelector('.gift-clip-crop-guides')).opacity) === 0);

  await resizeCropToMinimum(page);
  const desktopMinimumTargetability = await inspectMinimumCropTargetability(page, 'desktop');
  await page.getByRole('button', { name: '恢复完整画面' }).click();
  await waitForCropStatus(page, '640 × 360');

  await dragBy(page, page.locator('.gift-clip-crop-handle.is-w'), 96, 0);
  await waitForCropStatus(page, '512 × 360');

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
  const mobileVisualContract = await page.evaluate(() => {
    const handle = document.querySelector('.gift-clip-crop-handle');
    const stage = document.querySelector('.gift-clip-stage');
    if (!(handle instanceof HTMLElement)) throw new Error('.gift-clip-crop-handle missing');
    if (!(stage instanceof HTMLElement)) throw new Error('.gift-clip-stage missing');
    const handleStyle = getComputedStyle(handle);
    return {
      handleWidth: handleStyle.width,
      handleHeight: handleStyle.height,
      stageRadius: getComputedStyle(stage).borderRadius,
    };
  });
  assert.equal(mobileVisualContract.handleWidth, '32px');
  assert.equal(mobileVisualContract.handleHeight, '32px');
  assert.equal(mobileVisualContract.stageRadius, '0px');
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

  await resizeCropToMinimum(page);
  const mobileMinimumTargetability = await inspectMinimumCropTargetability(page, 'mobile');
  const minimumTargetabilityFailures = [
    ...desktopMinimumTargetability.failures,
    ...mobileMinimumTargetability.failures,
  ];
  assert.deepEqual(
    minimumTargetabilityFailures,
    [],
    `minimum crop targetability failures:\n${minimumTargetabilityFailures.join('\n')}`,
  );

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
