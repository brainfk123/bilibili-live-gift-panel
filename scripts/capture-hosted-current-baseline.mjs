import assert from 'node:assert/strict';
import {mkdir,readFile,writeFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import {join} from 'node:path';
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {createServer} from 'vite';
import {chromium} from 'playwright';
import {buildPopulatedShards} from './exe-populated-fixture.mjs';

// Capture the shipped Hosted frontend with a deterministic, explicitly synthetic
// HTTP transport. This does not exercise login, MySQL, or the migration decoder.
const root=fileURLToPath(new URL('..',import.meta.url));
const fixtureBase='acceptance/exe-hosted-ui/fixtures/';
const emptyBytes=await readFile(join(root,fixtureBase+'empty-0.4.10.json'));
const populatedBytes=await readFile(join(root,fixtureBase+'populated-0.4.10.json'));
const shards=buildPopulatedShards(JSON.parse(emptyBytes),JSON.parse(populatedBytes));
const source=shards['config.json'];
const ids=new Map(source.attributes.map(attribute=>[attribute.name,attribute.id]));
const keyed=object=>Object.fromEntries(Object.entries(object).map(([name,value])=>[ids.get(name),value]));
const configuration={version:1,revision:1,definition:{
  attributes:source.attributes.map(({value,...attribute})=>attribute),
  displayScenes:source.displayScenes.map(({attributeNames,...scene})=>({...scene,attributeIds:attributeNames.map(name=>ids.get(name))})),
  giftTargetPanels:source.giftKpiPanels.map(({id,name,layout,items})=>({id,name,layout,items:items.map(item=>({giftId:item.giftId,name:item.giftName,target:item.target,barStyle:item.barStyle}))})),
  activities:source.activities.map(({attributeNames,initialValues,status,...activity})=>({...activity,attributeIds:attributeNames.map(name=>ids.get(name)),initialValues:keyed(initialValues)})),
  rules:source.rules.map(({attributeName,...rule})=>({...rule,attributeId:ids.get(attributeName)})),
  timerRules:[],formulaPresets:[],gifts:shards['cache.json'].giftCatalog.map(({imgBasic,...gift})=>gift),
},runtime:{
  attributeValues:Object.fromEntries(source.attributes.map(attribute=>[attribute.id,attribute.value])),
  giftTargetReceived:source.giftKpiPanels.flatMap(panel=>panel.items.map(item=>({panelId:panel.id,giftId:item.giftId,received:shards['history.json'].giftTargetProgress[panel.id][String(item.giftId)]}))),
  activities:source.activities.map(activity=>({id:activity.id,status:activity.status,milestones:[]})),
  ruleLimits:{localDate:'2026-09-01',appliedCounts:{}},
}};
const streams=new Set();
const server=await createServer({root,configFile:join(root,'vite.hosted.config.ts'),server:{host:'127.0.0.1',port:0},plugins:[{
  name:'local-ui-fixture-transport',configureServer(vite){
    vite.middlewares.use((request,response,next)=>{
      const path=new URL(request.url,'http://fixture.invalid').pathname;
      if(!path.startsWith('/api/')) return next();
      if(request.method!=='GET'){response.statusCode=405;response.end();return;}
      if(path==='/api/runtime/events') {
        response.setHeader('Content-Type','text/event-stream');response.setHeader('Cache-Control','no-store');
        response.write(`event: status\ndata: ${JSON.stringify({state:'idle',leases:1,configLease:true,obsLease:false,degraded:false,connectionHealthy:true})}\n\n`);
        streams.add(response);response.on('close',()=>streams.delete(response));return;
      }
      const bodies={'/api/bootstrap':{csrfToken:'local-ui-fixture-not-a-credential'},'/api/auth/session':{authenticated:true},'/api/configuration':configuration};
      if(!Object.hasOwn(bodies,path)){response.statusCode=404;response.end();return;}
      response.setHeader('Content-Type','application/json');response.end(JSON.stringify(bodies[path]));
    });
  },
}]});
let browser;
const directoryName=`acceptance/exe-hosted-ui/captures/0.4.10/${new Date().toISOString().replace(/[:.]/g,'-')}-hosted-current`;
const directory=join(root,directoryName),captures=[];
const sha=value=>createHash('sha256').update(value).digest('hex');
try {
  const api=await server.ssrLoadModule('/src/hosted/api.ts');
  assert.ok(api.isHostedConfigurationDefinition(configuration.definition));
  assert.ok(api.isHostedConfigurationRuntime(configuration.runtime));
  await server.listen();
  const address=server.httpServer.address();
  const origin=`http://127.0.0.1:${address.port}`;
  await mkdir(directory,{recursive:true});
  browser=await chromium.launch();
  const contract=JSON.parse(await readFile(join(root,'acceptance/exe-hosted-ui/requirements.json')));
  for(const viewport of contract.viewports) {
    const page=await browser.newPage({viewport:{width:viewport.width,height:viewport.height},deviceScaleFactor:1,locale:'zh-CN',timezoneId:'Asia/Shanghai'});
    const errors=[];page.on('pageerror',error=>errors.push(error.message));
    await page.goto(origin+'/hosted.html',{waitUntil:'domcontentloaded'});
    await page.getByRole('heading',{name:'主播账号',exact:true}).waitFor();
    await page.getByText('运行状态：等待选择直播间',{exact:true}).waitFor();
    for(const view of ['account','configuration-json']) {
      if(view==='configuration-json') {
        await page.getByRole('button',{name:'在线配置',exact:true}).click();
        await page.getByRole('heading',{name:'在线配置',exact:true}).waitFor();
        await page.getByLabel('服务器权威配置对照').filter({hasText:'红队积分'}).waitFor();
      }
      await page.waitForFunction(()=>document.fonts.status==='loaded',null,{timeout:10_000});
      const name=`hosted-${view}-populated-${viewport.id}.png`;
      await page.screenshot({path:join(directory,name),animations:'disabled'});
      assert.deepEqual(errors,[]);
      captures.push({view,viewport:viewport.id,path:`${directoryName}/${name}`,sha256:sha(await readFile(join(directory,name)))});
    }
    await page.close();
  }
  const report={schema:1,evidenceMode:'actual Hosted frontend with synthetic HTTP/SSE transport; no real login, database or migration validation',
    commit:execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim(),browser:browser.version(),scriptSHA256:sha(await readFile(fileURLToPath(import.meta.url))),
    fixtures:[{path:fixtureBase+'empty-0.4.10.json',sha256:sha(emptyBytes)},{path:fixtureBase+'populated-0.4.10.json',sha256:sha(populatedBytes)}],
    projection:'same gameplay IDs, attribute values, activity, target and progress; names converted to IDs for current Hosted DTO',
    unsupportedInCurrentDTO:['global appearance','blind-box appearance','gift target appearance','viewer contributions','gift receipt history'],captures};
  await writeFile(join(directory,'manifest.json'),JSON.stringify(report,null,2)+'\n');
  console.log(JSON.stringify({directory:directoryName,captures:captures.length}));
} finally {
  try {await browser?.close();} finally {for(const stream of streams)stream.end();await server.close();}
}
