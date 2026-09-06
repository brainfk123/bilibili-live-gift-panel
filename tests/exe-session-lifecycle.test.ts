import {describe,it,expect,vi} from 'vitest';
import {beginDisposableSession,finishDisposableSession} from '../scripts/exe-session-lifecycle.mjs';
describe('EXE capture recovery',()=>{
  it('restores after the original stopped but its exit response was lost',async()=>{
    const restore=vi.fn(async()=>{}),startFixture=vi.fn(async()=>{});
    await expect(beginDisposableSession({stopOriginal:async()=>{throw new Error('exit response lost');},startFixture,verifyFixture:async()=>({}),restore})).rejects.toThrow('exit response lost');
    expect(restore).toHaveBeenCalledTimes(1);expect(startFixture).not.toHaveBeenCalled();
  });
  it('restores if loaded fixture verification fails',async()=>{
    const restore=vi.fn(async()=>{});
    await expect(beginDisposableSession({stopOriginal:async()=>{},startFixture:async()=>{},verifyFixture:async()=>{throw new Error('wrong progress');},restore})).rejects.toThrow('wrong progress');
    expect(restore).toHaveBeenCalledTimes(1);
  });
  it('still restores and verifies if browser teardown fails',async()=>{
    const restore=vi.fn(async()=>{}),verifyOriginal=vi.fn(async()=>{});
    const result=await finishDisposableSession({closeBrowser:async()=>{throw new Error('browser died');},restore,verifyOriginal});
    expect(result.restored).toBe(true);expect(result.errors).toEqual([{phase:'browser cleanup',message:'browser died'}]);
    expect(restore).toHaveBeenCalledTimes(1);expect(verifyOriginal).toHaveBeenCalledTimes(1);
  });
  it('never reports restoration success when original-state verification fails',async()=>{
    const result=await finishDisposableSession({closeBrowser:async()=>{},restore:async()=>{},verifyOriginal:async()=>{throw new Error('state changed');}});
    expect(result.restored).toBe(false);expect(result.errors[0].phase).toBe('EXE restoration');
  });
});
