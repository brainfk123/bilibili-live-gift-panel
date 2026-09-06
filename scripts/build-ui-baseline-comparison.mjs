import {readFile,writeFile,mkdir} from 'node:fs/promises';
import {join,relative,dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
const root=fileURLToPath(new URL('..',import.meta.url));
const read=async name=>JSON.parse(await readFile(join(root,`acceptance/exe-hosted-ui/reports/2026-09-06-${name}.json`)));
const [exe,hosted,coverage]=await Promise.all(['populated','hosted','coverage'].map(read));
const output=join(root,'acceptance/exe-hosted-ui/captures/0.4.10/comparison-2026-09-06.html');
const escape=text=>String(text).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('"','&quot;');
const image=record=>{
  if(!record.path.startsWith('acceptance/exe-hosted-ui/captures/0.4.10/')||record.path.includes('..')) throw new Error('Invalid screenshot path');
  const src=escape(relative(dirname(output),join(root,record.path)));
  return `<a href="${src}"><img loading="lazy" src="${src}" alt="${escape(record.feature??record.view)} ${escape(record.viewport)}"></a>`;
};
const sections=[];
for(const viewport of ['desktop-1440x900','narrow-1024x768','mobile-390x844']) {
  for(const [feature,view,title] of [['overview','account','概览 / Hosted 账号页'],['attributes','configuration-json','属性工作台 / Hosted JSON 配置页']]) {
    const a=exe.captures.find(x=>x.feature===feature&&x.state==='populated'&&x.viewport===viewport);
    const b=hosted.captures.find(x=>x.view===view&&x.viewport===viewport);
    sections.push(`<section><h2>${title} · ${viewport}</h2><div class="pair"><figure><figcaption>Windows EXE → macOS Chromium</figcaption>${image(a)}</figure><figure><figcaption>Hosted 实际前端 · 合成 HTTP/SSE 接口</figcaption>${image(b)}</figure></div></section>`);
  }
}
await mkdir(dirname(output),{recursive:true});
await writeFile(output,`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>EXE / Hosted 当前差距</title><style>body{font:16px system-ui;margin:32px;background:#101116;color:#eee}h1{font-size:28px}.note{max-width:1000px;line-height:1.7;color:#c9cad1}.pair{display:grid;grid-template-columns:1fr 1fr;gap:16px}figure{margin:0;background:#22242c;padding:12px;border-radius:10px}figcaption{margin-bottom:10px}img{display:block;max-width:100%;height:auto}section{margin-top:36px}a{color:inherit}@media(max-width:700px){.pair{grid-template-columns:1fr}}</style><h1>EXE 与 Hosted 当前差距</h1><p class="note">比较结论：尚未对齐。左侧运行正式 v0.4.10 EXE，右侧运行当前 Hosted 前端；两端使用同一浏览器和对应玩法夹具。Hosted 截图不验证真实登录、数据库和导入器；其 DTO 尚不支持完整外观及观众历史。点击图片可查看原始尺寸。</p><p class="note">EXE 合同状态覆盖 ${coverage.captured}，待补 ${coverage.pending}。102 张有数据/交互截图及 18 张空状态截图保留在本地；此页只选择两组直接对比，不代表六工作区均已通过。</p>${sections.join('')}<p class="note">下一阶段：先补齐迁移协议与数据模型的兼容性，再逐工作区替换 JSON 编辑入口并按状态验收。</p></html>`);
console.log(relative(root,output));
