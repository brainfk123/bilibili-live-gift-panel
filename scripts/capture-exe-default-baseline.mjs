import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
import { chromium } from 'playwright';
import { assertEmptyBaseline } from './exe-baseline-fixture.mjs';

// This command captures the actual fixed EXE, not a local build of its frontend.
// Run only against the isolated baseline VM; --seed explicitly replaces empty config.
const root = fileURLToPath(new URL('..', import.meta.url));
const base = new URL(process.env.EXE_BASE_URL ?? 'http://10.211.55.3:12451');
assert.equal(base.origin, 'http://10.211.55.3:12451', 'Expected the configured host-only baseline VM');
const fixturePath = 'acceptance/exe-hosted-ui/fixtures/empty-0.4.10.json';
const fixtureBytes = await readFile(join(root, fixturePath));
const fixture = JSON.parse(fixtureBytes);
const contract = JSON.parse(await readFile(join(root, 'acceptance/exe-hosted-ui/requirements.json')));
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
const request = async (path, options) => {
  const response = await fetch(new URL(path, base), { signal: AbortSignal.timeout(10_000), ...options });
  assert.ok(response.ok, `EXE request failed: ${response.status}`);
  return response.json();
};
assert.equal((await request('/health')).version, '0.4.10');
const current = await request('/api/config');
assertEmptyBaseline(current);
const runID = new Date().toISOString().replace(/[:.]/g, '-');
const relativeDirectory = `acceptance/exe-hosted-ui/captures/0.4.10/${runID}`;
const directory = join(root, relativeDirectory);
await mkdir(directory, {recursive:true});
if (process.argv.includes('--seed')) {
  // Preserve presentation preferences before explicit fixture preparation.
  await writeFile(join(directory,'before-seed.json'),JSON.stringify(current,null,2)+'\n');
  await request('/api/config', {method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(fixture)});
}
const seeded = await request('/api/config');
for (const [key,value] of Object.entries(fixture)) assert.deepEqual(seeded[key], value, `Fixture mismatch: ${key}; prepare the isolated VM with --seed`);

const routes = [
  ['overview','overview','.overview-dashboard'],
  ['attributes','attributes','.attributes-section'],
  ['activities','activities','.activity-workspace-section'],
  ['gift-targets','kpi','.gift-kpi-config-section'],
  ['obs','obs','.obs-panel-hub'],
  ['analytics','data','.contribution-section'],
];
const browser = await chromium.launch();
const captures = [];
try {
  for (const viewport of contract.viewports) {
    const context = await browser.newContext({viewport:{width:viewport.width,height:viewport.height},deviceScaleFactor:viewport.deviceScaleFactor});
    try {
      for (const [feature,route,section] of routes) {
        const page = await context.newPage();
        const errors = [];
        page.on('pageerror', error => errors.push(error.message));
        const response = await page.goto(new URL(`/?mode=config&page=${route}`,base).href,{waitUntil:'domcontentloaded'});
        assert.equal(response.status(),200);
        await page.locator(section).waitFor({state:'visible'});
        await page.evaluate(() => document.fonts.ready);
        assert.equal(await page.locator('.overlay:visible,.tour-bubble:visible').count(),0,'An overlay invalidates the empty-state capture');
        const filename = `exe-${feature}-empty-${viewport.id}.png`;
        // Exclude environment-specific OBS URL fields, not page content or controls.
        const urls = page.locator('input').filter({visible:true});
        await page.screenshot({path:join(directory,filename),animations:'disabled',mask:feature==='obs'?[urls]:[]});
        assert.deepEqual(errors,[],'Browser errors invalidate the capture');
        captures.push({feature,state:'empty',viewport:viewport.id,path:`${relativeDirectory}/${filename}`,sha256:sha256(await readFile(join(directory,filename))),redactions:feature==='obs'?['visible input values (OBS URLs)']:[]});
        await page.close();
      }
    } finally { await context.close(); }
  }
  const manifest = {
    schema:1, scope:'EXE empty state only; Hosted comparison and remaining states pending',
    exeVersion:'0.4.10', exeCommit:execFileSync('git',['rev-parse','v0.4.10^{commit}'],{cwd:root,encoding:'utf8'}).trim(),
    expectedExeSHA256:'12649c86fc8492dbd6c75ff62df22e37116adb269a24af4c9f1167bf6f283b53',
    referenceSnapshot:'f7d18a0e-09d5-4add-b022-de7fa9c2b074',
    identityVerification:'health version checked; artifact hash and snapshot restoration must be independently verified',
    fixture:{path:fixturePath,sha256:sha256(fixtureBytes)},
    browser:browser.version(),browserPlatform:'macOS',deviceScaleFactor:1,createdAt:new Date().toISOString(),
    captureCommit:execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim(),
    captureScriptSHA256:sha256(await readFile(fileURLToPath(import.meta.url))),
    captures,
  };
  await writeFile(join(directory,'manifest.json'),JSON.stringify(manifest,null,2)+'\n');
  console.log(JSON.stringify({directory:relativeDirectory,captures:captures.length}));
} finally { await browser.close(); }
