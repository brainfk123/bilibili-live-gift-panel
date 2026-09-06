import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {mkdir,readFile,writeFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import {join} from 'node:path';
import {chromium,expect} from 'playwright/test';
import {assertEmptyBaseline} from './exe-baseline-fixture.mjs';
import {buildPopulatedShards} from './exe-populated-fixture.mjs';
import {exeOrigin,exeRequest,startFixtureSession} from './exe-vm-session.mjs';
import {finishDisposableSession} from './exe-session-lifecycle.mjs';

const root=fileURLToPath(new URL('..',import.meta.url));
const fixtureRoot='acceptance/exe-hosted-ui/fixtures/';
const emptyBytes=await readFile(join(root,fixtureRoot+'empty-0.4.10.json'));
const specBytes=await readFile(join(root,fixtureRoot+'populated-0.4.10.json'));
const empty=JSON.parse(emptyBytes),spec=JSON.parse(specBytes);
const contract=JSON.parse(await readFile(join(root,'acceptance/exe-hosted-ui/requirements.json')));
const hash=value=>createHash('sha256').update(value).digest('hex');
const relativeDirectory=`acceptance/exe-hosted-ui/captures/0.4.10/${new Date().toISOString().replace(/[:.]/g,'-')}-populated`;
const directory=join(root,relativeDirectory); await mkdir(directory,{recursive:true});
const original=await exeRequest('/api/config'); assertEmptyBaseline(original);
const session=await startFixtureSession(buildPopulatedShards(empty,spec),directory);
let browser;
const captures=[],interactions=[],failures=[];
let restored=false;
try {
  browser=await chromium.launch();
  for(const viewport of contract.viewports) {
    const context=await browser.newContext({viewport:{width:viewport.width,height:viewport.height},deviceScaleFactor:1,locale:'zh-CN',timezoneId:'Asia/Shanghai'});
    context.setDefaultTimeout(10_000);
    for(const [feature,route,section] of [
      ['overview','overview','.overview-dashboard'],['attributes','attributes','.attributes-section'],
      ['activities','activities','.activity-workspace-section'],['gift-targets','kpi','.gift-kpi-config-section'],
      ['obs','obs','.obs-panel-hub'],['analytics','data','.contribution-section'],
    ]) {
      const page=await context.newPage(),errors=[];
      page.on('pageerror',error=>errors.push(error.message));
      const steps=[];
      const shot=async(state,extra={},target=page)=>{
        await target.waitForFunction(()=>document.fonts.status==='loaded',null,{timeout:10_000});
        const filename=`exe-${feature}-${state}-${viewport.id}.png`;
        // Redact URL-bearing output fields in all workspaces, including editor code previews.
        const masks=[target.locator('input[readonly]:visible'),target.locator('.output-link-preview code:visible')];
        await target.screenshot({path:join(directory,filename),animations:'disabled',mask:masks});
        assert.deepEqual(errors,[],'Unexpected browser script error');
        captures.push({feature,state,viewport:viewport.id,path:`${relativeDirectory}/${filename}`,sha256:hash(await readFile(join(directory,filename))),steps:[...steps],redactions:['readonly URL fields and output-link previews'],...extra});
      };
      const click=async(locator,label)=>{steps.push(label);await locator.click();};
      const feedbackVisibility=async(locator)=>{
        await expect(locator).toHaveClass(/show/);
        await expect(locator).toHaveCSS('opacity','1');
        return locator.evaluate(element=>{
          const box=element.getBoundingClientRect();
          const top=document.elementFromPoint(box.x+box.width/2,box.y+box.height/2);
          return {message:element.textContent,occluded:!top||!element.contains(top)};
        });
      };
      try {
        // Only the disposable runtime is reset. Backend-owned receipts/progress remain seeded.
        await exeRequest('/api/config',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(session.loaded)});
        await page.goto(`${exeOrigin}/?mode=config&page=${route}`,{waitUntil:'domcontentloaded'});
        steps.push(`Navigate to workspace ${feature}`);
        await page.locator(section).waitFor({state:'visible'});
        assert.equal(await page.locator('.overlay:visible,.tour-bubble:visible').count(),0);
        await shot('populated');
        // Use actual keyboard navigation to show a browser focus-visible state.
        await page.keyboard.press('Tab'); steps.push('Press Tab');
        assert.ok(await page.locator(':focus-visible').count()>0);
        await shot('focus-visible');
        if(feature==='overview') {
          await click(page.getByRole('button',{name:'程序与数据',exact:true}),'Open program settings');
          await page.getByRole('dialog',{name:'程序与数据',exact:true}).waitFor();
          await shot('overlay-open');
          await click(page.getByRole('button',{name:'关闭程序与数据'}),'Close program settings');
          interactions.push({feature,viewport:viewport.id,action:'open-settings',result:'passed'});
        } else if(feature==='attributes') {
          const card=page.locator('.attribute-card').first(); await card.hover();
          await click(card.getByRole('button',{name:'编辑',exact:true}),'Edit red-team attribute');
          const dialog=page.getByRole('dialog',{name:'编辑属性 红队积分',exact:true});await dialog.waitFor();
          await shot('editing');
          await dialog.getByLabel('属性名称',{exact:true}).fill(''); steps.push('Clear attribute name');
          await click(dialog.getByRole('button',{name:'保存修改',exact:true}),'Submit empty attribute name');
          const attributeFeedback=page.getByText('请填写属性名称',{exact:true});
          await attributeFeedback.waitFor();
          await shot('validation-error',{feedback:await feedbackVisibility(attributeFeedback)});
          await click(dialog.getByRole('button',{name:'取消',exact:true}),'Cancel invalid attribute draft');
          assert.equal((await exeRequest('/api/config')).attributes[0].name,'红队积分');
          await page.getByRole('button',{name:'+ 添加属性',exact:true}).click();steps.push('Create attribute');
          await click(page.getByRole('button',{name:/空白/}),'Choose blank attribute instead of a template');
          const create=page.getByRole('dialog',{name:'添加属性',exact:true});await create.waitFor();
          await shot('overlay-open');
          await create.getByLabel('属性名称',{exact:true}).fill('基线新增积分');
          await click(create.getByRole('button',{name:'创建属性',exact:true}),'Save new synthetic attribute');
          await create.waitFor({state:'hidden'});
          assert.ok((await exeRequest('/api/config')).attributes.some(x=>x.name==='基线新增积分'));
          interactions.push({feature,viewport:viewport.id,action:'create/edit/validation/cancel/save',result:'passed'});
          const added=page.locator('.attribute-card').filter({has:page.getByRole('heading',{name:'基线新增积分',exact:true})});
          await added.hover(); await click(added.getByRole('button',{name:'删除',exact:true}),'Arm deletion of synthetic attribute');
          await added.getByRole('button',{name:'确定',exact:true}).waitFor();
          await shot('delete-confirm');
          await click(added.getByRole('button',{name:'确定',exact:true}),'Confirm deletion of synthetic attribute');
          await added.waitFor({state:'hidden'});
          interactions.push({feature,viewport:viewport.id,action:'delete-confirm',result:'passed'});
        } else if(feature==='activities') {
          let card=page.locator('.activity-card').first();await card.hover();
          await click(card.getByRole('button',{name:'编辑',exact:true}),'Edit fixture activity');
          const dialog=page.getByRole('dialog',{name:'编辑活动 基线友谊赛',exact:true});await dialog.waitFor();await shot('overlay-open');
          await click(dialog.getByRole('button',{name:'取消',exact:true}),'Cancel activity draft');
          for(const [label,status] of [['开始活动','active'],['锁定结果','locked'],['确认结算','settled']]) {
            card=page.locator('.activity-card').first();await card.hover();
            await click(card.getByRole('button',{name:label,exact:true}),label);
            await page.locator(`.activity-card.is-${status}`).waitFor();
            assert.equal((await exeRequest('/api/config')).activities[0].status,status);
            await shot(status);
            interactions.push({feature,viewport:viewport.id,action:{active:'start',locked:'lock',settled:'settle'}[status],result:'passed'});
          }
        } else if(feature==='gift-targets') {
          const card=page.locator('.gift-kpi-config-card').first();await card.hover();
          await click(card.getByRole('button',{name:'编辑',exact:true}),'Edit gift target');
          const dialog=page.getByRole('dialog',{name:'编辑礼物目标面板'});await dialog.waitFor();await shot('editing');
          await dialog.getByLabel('面板名称',{exact:true}).fill('');
          await click(dialog.getByRole('button',{name:'保存修改',exact:true}),'Submit empty target name');
          const targetFeedback=page.getByText('请填写名称并至少选择一种礼物',{exact:true});
          await targetFeedback.waitFor();await shot('validation-error',{feedback:await feedbackVisibility(targetFeedback)});
          await click(dialog.getByRole('button',{name:'取消',exact:true}),'Cancel target draft');
          await click(page.getByRole('button',{name:'+ 新建目标面板',exact:true}),'Open new target editor');
          const create=page.getByRole('dialog',{name:'新建礼物目标面板'});await create.waitFor();await shot('overlay-open');
          await click(create.getByRole('button',{name:'取消',exact:true}),'Cancel new target');
          await card.hover();await click(card.getByRole('button',{name:'删除',exact:true}),'Arm target deletion without confirming');await shot('delete-confirm');
          interactions.push({feature,viewport:viewport.id,action:'edit/validation/cancel/delete-confirm',result:'passed'});
        } else if(feature==='obs') {
          const card=page.locator('.display-scene-card').first();await card.scrollIntoViewIfNeeded();await card.focus();await page.keyboard.press('Tab');steps.push('Focus combination card using keyboard');
          await click(card.getByRole('button',{name:'编辑',exact:true}),'Edit combination output');
          const dialog=page.getByRole('dialog',{name:'编辑组合面板 双队积分板'});await dialog.waitFor();await shot('overlay-open');
          await click(dialog.getByRole('button',{name:'取消',exact:true}),'Cancel combination draft');
          const output=await context.newPage();output.on('pageerror',e=>errors.push(e.message));
          let previewURL;
          try {
            await output.goto(`${exeOrigin}/?mode=config&page=obs`,{waitUntil:'domcontentloaded'});
            await output.locator('.obs-panel-hub').waitFor();
            await output.mouse.move(0,0);
            const outputCard=output.locator('.display-scene-card').first();
            await expect(async()=>{await outputCard.scrollIntoViewIfNeeded({timeout:2000});}).toPass({timeout:10_000});
            await outputCard.focus();await output.keyboard.press('Tab');
            const link=output.locator('.display-scene-url').first();await link.waitFor({state:'visible'});
            steps.push('Open OBS in a fresh page, scroll to and focus its readonly output link');
            await shot('readonly',{stateScope:'readonly output-link field'},output);
            previewURL=new URL(await link.inputValue());
          } finally {await output.close();}
          assert.equal(previewURL.origin,exeOrigin);
          const preview=await context.newPage();preview.on('pageerror',e=>errors.push(e.message));
          try {
            await preview.goto(previewURL.href,{waitUntil:'domcontentloaded'});
            await preview.getByText('红队积分',{exact:true}).first().waitFor();
            steps.push('Open the combination output URL read from its readonly field');
            await shot('preview',{},preview);
          } finally {await preview.close();}
          interactions.push({feature,viewport:viewport.id,action:'edit/cancel',result:'passed'});
        } else if(feature==='analytics') {
          await page.locator('.gift-history-row').first().waitFor();
          await expect(page.locator('.gift-history-row')).toHaveCount(40);
          await click(page.getByRole('tab',{name:'规则命中',exact:true}),'Filter ranking to rule contributions');await shot('filtered');
          const loader=page.locator('.gift-history-loader');await loader.scrollIntoViewIfNeeded();
          steps.push('Scroll receipt list to trigger automatic next batch');
          await expect(page.locator('.gift-history-row')).toHaveCount(45);
          await shot('paginated');
          assert.equal(await page.getByRole('button',{name:'无动画素材',exact:true}).first().isDisabled(),true);await shot('disabled',{stateScope:'media replay button without animation assets'});
          await click(page.getByRole('button',{name:'清空记录',exact:true}),'Arm clearing synthetic receipt history');await shot('clear-confirm');
          interactions.push({feature,viewport:viewport.id,action:'filter/paginate/clear-confirm',result:'passed'});
          let release;
          const pending=new Promise(resolve=>{release=resolve;});
          await page.route('**/api/blind-box/leaderboard*',async request=>{
            await pending;
            await request.fulfill({status:503,contentType:'application/json',body:JSON.stringify({code:-1,message:'Baseline injected service failure'})});
          });
          try {
            await page.reload({waitUntil:'domcontentloaded'});
            steps.push('Hold the leaderboard response using browser request interception');
            await page.locator('.blind-box-leaderboard-status:not(.is-error)').waitFor();
            await shot('loading',{evidenceMode:'browser fault injection on actual EXE UI'});
            release();steps.push('Return a deliberate HTTP 503 leaderboard response');
            await page.locator('.blind-box-leaderboard-status.is-error').waitFor();
            await shot('error',{evidenceMode:'browser fault injection on actual EXE UI'});
          } finally {release();}
        }
        console.log(`${viewport.id} ${feature}: captured`);
      } catch(error) {
        failures.push({feature,viewport:viewport.id,steps,error:error.message.split('\n')[0]});
        console.error(`${viewport.id} ${feature}: ${error.message}`);
        await page.screenshot({path:join(directory,`diagnostic-${feature}-${viewport.id}.png`),mask:[page.locator('input[readonly]:visible'),page.locator('.output-link-preview code:visible')]}).catch(()=>undefined);
      } finally {await page.close();}
    }
    await context.close();
  }
} finally {
  const cleanup=await finishDisposableSession({closeBrowser:async()=>{await browser?.close();},restore:session.restore,verifyOriginal:async()=>{assert.deepEqual(await exeRequest('/api/config'),original,'Original EXE state changed');}});
  restored=cleanup.restored;failures.push(...cleanup.errors);
  const report={schema:1,scope:'Actual EXE populated and interactive states; no Hosted parity claim',referenceSnapshot:'f7d18a0e-09d5-4add-b022-de7fa9c2b074',exeSHA256:session.exeSHA256,session:session.id,shards:session.files,
    fixtures:[{path:fixtureRoot+'empty-0.4.10.json',sha256:hash(emptyBytes)},{path:fixtureRoot+'populated-0.4.10.json',sha256:hash(specBytes)}],
    browser:browser?.version(),browserPlatform:'macOS',deviceScaleFactor:1,timezone:'Asia/Shanghai',locale:'zh-CN',
    commit:execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim(),scriptSHA256:hash(await readFile(fileURLToPath(import.meta.url))),captures,interactions,failures,restored};
  report.sourceFiles=await Promise.all(['capture-exe-populated-baseline.mjs','exe-vm-session.mjs','exe-session-lifecycle.mjs','exe-populated-fixture.mjs'].map(async name=>({path:`scripts/${name}`,sha256:hash(await readFile(join(root,'scripts',name)))})));
  await writeFile(join(directory,'manifest.json'),JSON.stringify(report,null,2)+'\n');
  console.log(JSON.stringify({directory:relativeDirectory,captures:captures.length,failures,restored}));
}
if(failures.length) process.exitCode=1;
