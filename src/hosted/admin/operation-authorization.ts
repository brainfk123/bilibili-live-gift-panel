import{HostedAPIError,type HostedAPI}from'../api';
export type OperationPurpose='bili_service_replace'|'admin_email_change'|'recovery_regenerate';
export async function authorizeAdminOperation(api:Pick<HostedAPI,'authorizeAdminOperation'>,input:{purpose:OperationPurpose;target:string;totp:string}):Promise<string>{if(!/^\d{6}$/.test(input.totp)||!input.target)throw new HostedAPIError('invalid_request',0);return api.authorizeAdminOperation(input.totp,input.purpose,input.target)}
