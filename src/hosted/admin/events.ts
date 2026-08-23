export const administratorEventLabel=(type:string):string=>({streamer_account_disabled:'账号已停用',streamer_account_enabled:'账号已启用',bili_service_credential_replaced:'B站服务账号已替换'}[type]??'管理员操作');
