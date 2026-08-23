import {describe,expect,it} from 'vitest';
import {HostedAPI} from '../src/hosted/api';
const json=(body:unknown,status=200)=>new Response(JSON.stringify(body),{status,headers:{'Content-Type':'application/json'}});
const overview={totalAccounts:3,activeAccounts:2,disabledAccounts:1,missingRooms:1,missingObs:1,attention:[{kind:'missing_room',accountId:41,text:'尚未设置直播间',priority:100}],recentEvents:[{type:'streamer_account_enabled',text:'账号已启用',accountId:41,createdAt:'2026-08-23T00:00:00Z'}]};
describe('administrator overview API',()=>{
 it('parses the exact safe projection',async()=>{const api=await HostedAPI.connect(async(input)=>input==='/api/bootstrap'?json({csrfToken:'csrf'}):json(overview));await expect(api.adminOverview()).resolves.toEqual(overview)});
 it('rejects unexpected identity and raw audit fields',async()=>{for(const extra of [{uid:'123'},{cookie:'secret'},{eventData:{raw:true}}]){const api=await HostedAPI.connect(async(input)=>input==='/api/bootstrap'?json({csrfToken:'csrf'}):json({...overview,...extra}));await expect(api.adminOverview()).rejects.toMatchObject({code:'invalid_response'})}});
});
