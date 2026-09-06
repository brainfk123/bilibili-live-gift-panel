import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
import {randomUUID,createHash} from 'node:crypto';
import {beginDisposableSession} from './exe-session-lifecycle.mjs';
import {verifyLoadedShards} from './exe-populated-fixture.mjs';

const vm='Windows 11';
export const exeOrigin='http://10.211.55.3:12451';
const exe='C:\\UIBaseline\\v0.4.10\\gift-panel-windows-x64.exe';
const expectedHash='12649c86fc8492dbd6c75ff62df22e37116adb269a24af4c9f1167bf6f283b53';
export async function exeRequest(path,options={}) {
  const response=await fetch(new URL(path,exeOrigin),{signal:AbortSignal.timeout(10_000),...options});
  assert.ok(response.ok,`EXE API ${path} failed (${response.status})`);
  return response.json();
}
function ps(script,currentUser=false) {
  return execFileSync('/usr/local/bin/prlctl',['exec',vm,...(currentUser?['--current-user']:[]),'powershell.exe','-NoProfile','-NonInteractive','-EncodedCommand',Buffer.from(`$ErrorActionPreference='Stop'; $ProgressPreference='SilentlyContinue'; ${script}`,'utf16le').toString('base64')],{encoding:'utf8',timeout:30_000,maxBuffer:1024*1024});
}
async function waitForState(running) {
  const deadline=Date.now()+20_000;
  while(Date.now()<deadline) {
    let available=false;
    try {available=(await exeRequest('/health',{signal:AbortSignal.timeout(500)})).version==='0.4.10';} catch {}
    if(available===running) return;
    await new Promise(resolve=>setTimeout(resolve,250));
  }
  throw new Error(`EXE did not become ${running?'ready':'stopped'}`);
}
async function stop() {
  await exeRequest('/api/instance/exit',{method:'POST',headers:{'X-Bilibili-Panel-Takeover':'dev'}});
  await waitForState(false);
}
export async function startFixtureSession(shards,directory) {
  assert.equal((await exeRequest('/health')).version,'0.4.10');
  const digest=execFileSync('/usr/local/bin/prlctl',['exec',vm,'certutil.exe','-hashfile',exe,'SHA256'],{encoding:'utf8',timeout:30_000});
  assert.ok(digest.includes(expectedHash),'Reference executable digest mismatch');
  const id=randomUUID();
  const guestRoot=`C:\\UIBaseline\\runs\\${id}`;
  const guestState=`${guestRoot}\\appdata\\BilibiliLiveGiftPanel`;
  const stage=join(directory,'staged-shards'); await mkdir(stage,{recursive:true});
  ps(`New-Item -ItemType Directory -Path '${guestState}' | Out-Null`);
  const files=[];
  for(const [name,value] of Object.entries(shards)) {
    assert.ok(['config.json','cache.json','history.json','events.log'].includes(name));
    const bytes=typeof value==='string'?value:JSON.stringify(value,null,2)+'\n';
    await writeFile(join(stage,name),bytes);
    files.push({name,sha256:createHash('sha256').update(bytes).digest('hex')});
    if(bytes.length===0) ps(`[IO.File]::WriteAllText('${guestState}\\${name}', '')`);
    else execFileSync('/usr/local/bin/prlcopy',['upload',join(stage,name),guestState,'--vm',vm],{timeout:30_000,stdio:'pipe'});
  }
  const restore=async()=>{
    // Only the disposable EXE process is stopped; the original data directory is untouched.
    try {await stop();} catch {await waitForState(false);}
    ps(`Start-Process -FilePath '${exe}'`,true);
    await waitForState(true);
  };
  const loaded=await beginDisposableSession({
    stopOriginal:stop,
    startFixture:async()=>{
      ps(`$env:APPDATA='${guestRoot}\\appdata'; $env:LOCALAPPDATA='${guestRoot}\\localappdata'; Start-Process -FilePath '${exe}'`,true);
      await waitForState(true);
    },
    verifyFixture:async()=>{const state=await exeRequest('/api/config');verifyLoadedShards(shards,state);return state;},
    restore,
  });
  return {restore,files,exeSHA256:expectedHash,id,loaded};
}
