import{describe,expect,it}from'vitest';import{HostedAPI,HostedAPIError}from'../src/hosted/api';
const json=(body:unknown,status=200)=>new Response(JSON.stringify(body),{status,headers:{'Content-Type':'application/json'}});

describe('administrator Bilibili service polling failures', () => {
  it('preserves the exact temporarily-unavailable transport classification', async () => {
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json({ error: 'temporarily_unavailable' }, 503));

    await expect(api.pollBiliServiceChallenge('private-proof')).rejects.toEqual(
      new HostedAPIError('temporarily_unavailable', 503),
    );
  });
});

describe('administrator Bilibili service challenge cancellation', () => {
  it('sends an encoded same-origin DELETE and accepts only an empty 204 response', async () => {
    const requests: Array<[RequestInfo | URL, RequestInit | undefined]> = [];
    let responseStatus = 204;
    const api = await HostedAPI.connect(async (input, init) => {
      requests.push([input, init]);
      if (input === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      return new Response(null, { status: responseStatus });
    });

    await api.cancelBiliServiceChallenge('proof/private?');

    expect(requests[1]).toEqual([
      '/api/admin/bili-service/challenge/proof%2Fprivate%3F',
      expect.objectContaining({
        method: 'DELETE',
        credentials: 'same-origin',
        headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf' }),
      }),
    ]);
    responseStatus = 200;
    await expect(api.cancelBiliServiceChallenge('proof')).rejects.toMatchObject({ code: 'invalid_response' });
  });
});
describe('controlled Bilibili service credential',()=>{it('parses only the redacted status projection',async()=>{const status={version:3,health:'healthy',maskedUid:'****9588',lastVerifiedAt:'2030-01-01T00:00:00Z',lastReplacedAt:'2030-01-01T00:00:00Z'};const connect=async(body:unknown)=>HostedAPI.connect(async(input)=>input==='/api/bootstrap'?json({csrfToken:'csrf'}):json(body));await expect((await connect(status)).biliServiceStatus()).resolves.toEqual(status);await expect((await connect({...status,cookie:'secret'})).biliServiceStatus()).rejects.toMatchObject({code:'invalid_response'})});it('sends operation authorization only in the replacement header',async()=>{const requests:Array<[RequestInfo|URL,RequestInit|undefined]>=[];const api=await HostedAPI.connect(async(input,init)=>{requests.push([input,init]);if(input==='/api/bootstrap')return json({csrfToken:'csrf'});if(input==='/api/admin/operation-authorizations')return json({authorizationToken:'single-use'},201);return new Response(null,{status:204})});const token=await api.authorizeAdminOperation('123456','bili_service_replace','global');await api.replaceBiliServiceCredential('challenge',token);expect(requests[2]?.[1]?.headers).toMatchObject({'X-Admin-Authorization':'single-use'});expect(requests[2]?.[1]?.body).toBe('{"challengeId":"challenge"}')});it('checks without exposing credentials',async()=>{const api=await HostedAPI.connect(async(input)=>input==='/api/bootstrap'?json({csrfToken:'csrf'}):json({version:0,health:'missing'}));await expect(api.checkBiliService()).resolves.toEqual({version:0,health:'missing'})});it('polls only the exact redacted service challenge stage',async()=>{const requests:Array<[RequestInfo|URL,RequestInit|undefined]>=[];let responseBody:unknown={status:'scanned'};const api=await HostedAPI.connect(async(input,init)=>{requests.push([input,init]);return input==='/api/bootstrap'?json({csrfToken:'csrf'}):json(responseBody)});await expect(api.pollBiliServiceChallenge('proof')).resolves.toEqual({status:'scanned'});expect(requests[1]?.[0]).toBe('/api/admin/bili-service/challenge/proof');for(const invalid of [{status:['scanned']},{status:'pending',uid:'must-not-cross'},{status:'verified',cookie:'must-not-cross'},{status:'scanned',expiresAt:'2030-01-01T00:00:00Z'},{status:'scanned',qrcode_key:'must-not-cross'},{status:'scanned',rawPayload:'must-not-cross'},{status:'scanned',challengeId:'must-not-cross'},{status:'scanned',unexpected:'must-not-cross'},{status:'expired'}]){responseBody=invalid;await expect(api.pollBiliServiceChallenge('proof')).rejects.toMatchObject({code:'invalid_response'})}})});
