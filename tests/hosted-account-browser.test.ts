import {fileURLToPath} from 'node:url';
import type {AddressInfo} from 'node:net';
import {createServer,type ViteDevServer} from 'vite';
import {chromium,type Browser,type Page} from 'playwright';
import {beforeAll,afterAll,it,expect} from 'vitest';
let server:ViteDevServer,browser:Browser,origin:string;
const definition={attributes:[{id:'red',name:'红队积分',unit:'none',format:'number',decimals:0,suffix:'分'},{id:'blue',name:'蓝队积分',unit:'none',format:'number',decimals:0,suffix:'分'}],displayScenes:[{id:'scene',name:'积分板',attributeIds:['red','blue'],layout:'grid',themeId:'glass'}],giftTargetPanels:[],activities:[],rules:[],timerRules:[],formulaPresets:[],gifts:[]};
const configuration={definition,runtime:{attributeValues:{red:23,blue:22},giftTargetReceived:[],activities:[],ruleLimits:{localDate:'2026-09-01',appliedCounts:{}}},version:1,revision:1};
beforeAll(async()=>{server=await createServer({root:fileURLToPath(new URL('..',import.meta.url)),configFile:false,server:{host:'127.0.0.1',port:0}});await server.listen();origin=`http://127.0.0.1:${(server.httpServer!.address() as AddressInfo).port}`;browser=await chromium.launch();},20_000);
afterAll(async()=>{try{await browser?.close();}finally{await server?.close();}});
async function apiFixture(page:Page,failFirst=false){
  let requests=0;
  await page.route('**/api/**',async route=>{
    const url=new URL(route.request().url());
    if(url.pathname==='/api/bootstrap')return route.fulfill({json:{csrfToken:'test-only'}});
    if(url.pathname==='/api/auth/session')return route.fulfill({json:{authenticated:true}});
    if(url.pathname==='/api/configuration')return ++requests===1&&failFirst?route.fulfill({status:503,json:{code:'temporarily_unavailable'}}):route.fulfill({json:configuration});
    if(url.pathname==='/api/runtime/events')return route.fulfill({contentType:'text/event-stream',body:'event: status\ndata: {"state":"idle","leases":1,"configLease":true,"obsLease":false,"degraded":false,"connectionHealthy":true}\n\n'});
    return route.fulfill({status:404,json:{code:'not_found'}});
  });
}
it.each([[1440,900],[1024,768],[390,844]])('renders an EXE-style account workbench without horizontal overflow at %sx%s',async(width,height)=>{
  const page=await browser.newPage({viewport:{width,height}});
  try {
    await apiFixture(page);await page.goto(origin+'/hosted.html');
    await page.getByRole('heading',{name:'直播控制台',exact:true}).waitFor();
    await expect.poll(()=>page.getByRole('button',{name:/属性玩法.*2 个/}).count()).toBe(1);
    expect(await page.getByRole('button',{name:/OBS 组合面板.*1 个/}).count()).toBe(1);
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
    expect((await page.getByRole('button',{name:'切换到浅色模式',exact:true}).boundingBox())!.width).toBeGreaterThanOrEqual(42);
    await page.getByRole('button',{name:'切换到浅色模式',exact:true}).click();
    expect(await page.evaluate(()=>document.documentElement.dataset.theme)).toBe('light');
    await page.getByRole('button',{name:'在线配置',exact:true}).click();
    await page.getByRole('heading',{name:'在线配置',exact:true}).waitFor();
  } finally {await page.close();}
});
it('clears the next view when an already-submitted logout completes after navigation',async()=>{
  const page=await browser.newPage();let release!:()=>void;
  const gate=new Promise<void>(resolve=>{release=resolve;});
  try {
    await apiFixture(page);
    await page.route('**/api/auth/session',async route=>{
      if(route.request().method()==='DELETE'){await gate;return route.fulfill({status:204});}
      return route.fulfill({json:{authenticated:true}});
    });
    await page.goto(origin+'/hosted.html');
    await page.getByRole('heading',{name:'直播控制台',exact:true}).waitFor();
    await page.getByRole('button',{name:'退出登录',exact:true}).click();
    await page.getByRole('button',{name:'在线配置',exact:true}).click();
    await page.getByRole('heading',{name:'在线配置',exact:true}).waitFor();
    release();
    await page.getByRole('button',{name:'使用 B 站账号登录',exact:true}).waitFor({timeout:2000});
    expect(await page.getByRole('heading',{name:'在线配置',exact:true}).count()).toBe(0);
  } finally {release();await page.close();}
});
it('shows unavailable statistics without inventing zero counts, and lets the user retry',async()=>{
  const page=await browser.newPage();
  try {
    await apiFixture(page,true);await page.goto(origin+'/hosted.html');
    await page.getByText('暂时无法读取配置统计。请重试。',{exact:true}).waitFor();
    expect(await page.getByRole('button',{name:/属性玩法.*0 个/}).count()).toBe(0);
    await page.getByRole('button',{name:'重试读取统计',exact:true}).click();
    await expect.poll(()=>page.getByRole('button',{name:/属性玩法.*2 个/}).count()).toBe(1);
  } finally {await page.close();}
});
