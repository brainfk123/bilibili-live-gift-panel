import {fileURLToPath} from 'node:url';
import type {AddressInfo} from 'node:net';
import {createServer,type ViteDevServer} from 'vite';
import {chromium,type Browser} from 'playwright';
import {beforeAll,afterAll,it,expect} from 'vitest';

let server:ViteDevServer,browser:Browser,origin:string;
beforeAll(async()=>{
  server=await createServer({root:fileURLToPath(new URL('..',import.meta.url)),configFile:false,server:{host:'127.0.0.1',port:0}});
  await server.listen();origin=`http://127.0.0.1:${(server.httpServer!.address() as AddressInfo).port}`;
  browser=await chromium.launch();
},20_000);
afterAll(async()=>{try{await browser?.close();}finally{await server?.close();}});

it('uses EXE dark/light surfaces and readable primary controls across Hosted feature families',async()=>{
  const page=await browser.newPage({viewport:{width:1440,height:900}});
  try {
    await page.route('**/theme-harness',route=>route.fulfill({contentType:'text/html',body:`<!doctype html><html><head><link rel="stylesheet" href="/src/hosted/shell.css"></head><body>
      <main class="hosted-shell hosted-panel"><h1>迁移本地配置</h1><label>输入<input value="配置名称"></label><button class="hosted-login">应用迁移</button></main>
      <section class="hosted-auth-card">登录</section>
      <section class="hosted-admin-content"><button class="hosted-admin-resource-card">邀请码</button></section>
      </body></html>`}));
    await page.goto(origin+'/theme-harness');
    const surfaces=page.locator('.hosted-panel,.hosted-auth-card,.hosted-admin-resource-card');
    expect(await surfaces.evaluateAll(es=>es.map(e=>getComputedStyle(e).backgroundColor))).toEqual(['rgb(34, 36, 46)','rgb(34, 36, 46)','rgb(34, 36, 46)']);
    expect(await page.locator('h1').evaluate(e=>getComputedStyle(e).fontSize)).toBe('32px');
    expect(await page.locator('.hosted-login').evaluate(e=>({background:getComputedStyle(e).backgroundColor,color:getComputedStyle(e).color}))).toEqual({background:'rgb(251, 114, 153)',color:'rgb(59, 16, 32)'});
    await page.evaluate(()=>{document.documentElement.dataset.theme='light';});
    await expect.poll(()=>surfaces.evaluateAll(es=>es.map(e=>getComputedStyle(e).backgroundColor))).toEqual(['rgb(255, 255, 255)','rgb(255, 255, 255)','rgb(255, 255, 255)']);
    expect(await page.locator('.hosted-login').evaluate(e=>({background:getComputedStyle(e).backgroundColor,color:getComputedStyle(e).color}))).toEqual({background:'rgb(194, 24, 91)',color:'rgb(255, 255, 255)'});
  } finally {await page.close();}
});

it('keeps selected admin navigation, bulk actions and danger hover readable in both themes',async()=>{
  const page=await browser.newPage({viewport:{width:1440,height:900},reducedMotion:'reduce'});
  page.setDefaultTimeout(1500);
  try {
    await page.route('**/admin-theme-harness',route=>route.fulfill({contentType:'text/html; charset=utf-8',body:`<!doctype html><html><head><link rel="stylesheet" href="/src/hosted/shell.css"></head><body><main class="hosted-admin-frame"><aside class="hosted-admin-sidebar"><button aria-current="page">主播账号</button></aside><section class="hosted-admin-content"><div class="hosted-admin-bulk-toolbar"><span>已选择 2 个账号</span><button>停用账号</button></div><button data-variant="danger-outline">撤销邀请</button></section></main></body></html>`}));
    await page.goto(origin+'/admin-theme-harness');
    for(const theme of ['dark','light']) {
      await page.evaluate(theme=>{document.documentElement.dataset.theme=theme;},theme);
      await page.getByRole('button',{name:'撤销邀请',exact:true}).hover();
      const contrasts=await page.locator('.hosted-admin-sidebar button[aria-current],.hosted-admin-bulk-toolbar,.hosted-admin-bulk-toolbar button,[data-variant="danger-outline"]').evaluateAll(elements=>{
        const parse=(value:string)=>value.match(/[\d.]+/g)!.map(Number);
        const background=(element:Element|null):number[]=>{
          if(!element)return [255,255,255];
          const color=parse(getComputedStyle(element).backgroundColor),alpha=color[3]??1;
          const behind=alpha===1?[0,0,0]:background(element.parentElement);
          return color.slice(0,3).map((n,i)=>n*alpha+behind[i]*(1-alpha));
        };
        const luminance=(values:number[])=>values.slice(0,3).map(n=>{const s=n/255;return s<=.04045?s/12.92:((s+.055)/1.055)**2.4;}).reduce((sum,n,i)=>sum+n*[.2126,.7152,.0722][i],0);
        return elements.map(element=>{const a=luminance(parse(getComputedStyle(element).color)),b=luminance(background(element));return (Math.max(a,b)+.05)/(Math.min(a,b)+.05);});
      });
      for(const ratio of contrasts)expect(ratio,`${theme} contrast`).toBeGreaterThanOrEqual(4.5);
    }
  } finally {await page.close();}
});
