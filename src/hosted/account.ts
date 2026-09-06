import {HostedAPIError,type HostedConfiguration} from './api';
import {mountRoomControls,type RoomRuntimePresence} from './room';
import {createBrandIcon} from '../ui/brand';
import type {HostedView} from './shell';

interface AccountAPI {
  loadConfiguration():Promise<HostedConfiguration>;
  setRuntimeRoom(roomID:string):Promise<void>;
  logout():Promise<void>;
}
interface AccountActions {
  onConfiguration():void;
  onMigration():void;
  onInvitations():void;
  onSignedOut():void;
}

/** Account presentation owns only DOM and pending reads; API and runtime own state. */
export function mountAccountView(root:HTMLElement,api:AccountAPI,presence:RoomRuntimePresence,actions:AccountActions):HostedView {
  const document=root.ownerDocument;
  const element=<K extends keyof HTMLElementTagNameMap>(tag:K,className:string,text?:string):HTMLElementTagNameMap[K]=>{
    const node=document.createElement(tag);node.className=className;if(text!==undefined)node.textContent=text;return node;
  };
  const button=(text:string,className:string,action:()=>void)=>{
    const node=element('button',className,text);node.type='button';node.addEventListener('click',action);return node;
  };
  let disposed=false,loading=false,loggingOut=false;
  const frame=element('div','hosted-workbench hosted-panel');
  const header=element('header','hosted-workbench-header');
  const brand=element('div','hosted-workbench-brand');
  const brandCopy=element('div','');
  brandCopy.append(element('strong','','直播礼物面板'),element('small','','在线工作台'));
  brand.append(createBrandIcon(36),brandCopy);
  const headerActions=element('div','hosted-workbench-actions');
  const theme=button('☀','hosted-icon-button',()=>{
    document.documentElement.dataset.theme=document.documentElement.dataset.theme==='light'?'dark':'light';syncTheme();
  });
  const syncTheme=()=>{const light=document.documentElement.dataset.theme==='light';theme.setAttribute('aria-label',light?'切换到深色模式':'切换到浅色模式');theme.title=theme.getAttribute('aria-label')!;theme.textContent=light?'☾':'☀';};
  syncTheme();
  const status=element('p','hosted-account-feedback');status.setAttribute('role','alert');
  const logout=button('退出登录','hosted-quiet',()=>{
    if(disposed||loggingOut)return;loggingOut=true;logout.disabled=true;logout.textContent='正在退出…';status.textContent='';
    // Logout invalidates the whole application, even if this view was replaced.
    // The owner guards document lifetime before mounting the signed-out view.
    void api.logout().then(()=>actions.onSignedOut()).catch(()=>{if(!disposed)status.textContent='退出失败，请稍后重试';}).finally(()=>{if(!disposed){loggingOut=false;logout.disabled=false;logout.textContent='退出登录';}});
  });
  headerActions.append(theme,element('span','hosted-status-pill','已登录'),logout);header.append(brand,headerActions);
  const layout=element('div','hosted-workbench-layout');
  const sidebar=element('aside','hosted-workbench-sidebar');
  const navigation=element('nav','hosted-workbench-navigation');navigation.setAttribute('aria-label','在线工作台');
  for(const [title,description,icon,action] of [
    ['概览','直播间与账号','◇',()=>{}],
    ['在线配置','玩法定义与当前状态','☷',actions.onConfiguration],
    ['迁移本地配置','预览、应用与回滚','⇄',actions.onMigration],
    ['我的邀请码','邀请与使用记录','✉',actions.onInvitations],
  ] as const) {
    const item=button('','hosted-workbench-nav',action);item.setAttribute('aria-label',title);
    if(title==='概览')item.setAttribute('aria-current','page');
    const copy=element('span','');copy.append(element('strong','',title),element('small','',description));
    const glyph=element('span','hosted-nav-glyph',icon);glyph.setAttribute('aria-hidden','true');item.append(glyph,copy);navigation.append(item);
  }
  sidebar.append(navigation,element('p','hosted-sidebar-note','配置在服务端保存。离开页面前请保存正在编辑的草稿。'));
  const content=element('main','hosted-workbench-content');
  const overview=element('section','hosted-surface hosted-overview');overview.setAttribute('aria-labelledby','hosted-overview-title');
  const heading=element('h1','','直播控制台');heading.id='hosted-overview-title';
  const intro=element('p','hosted-copy','先确认直播间，再进入玩法配置、迁移或邀请管理。');
  const stats=element('div','hosted-overview-stats');stats.setAttribute('aria-label','配置统计');
  const metrics=[['属性玩法','◇'],['活动会话','⚑'],['礼物目标','◎'],['OBS 组合面板','▣']].map(([title,icon])=>{
    const card=button('','hosted-summary-card',actions.onConfiguration);
    const glyph=element('span','hosted-summary-icon',icon);glyph.setAttribute('aria-hidden','true');
    const copy=element('span','hosted-summary-copy'),value=element('strong','','—'),description=element('small','','正在读取…');
    copy.append(element('span','hosted-summary-label',title),value,description);card.append(glyph,copy,element('span','hosted-summary-arrow','→'));stats.append(card);
    return {value,description};
  });
  const loadStatus=element('p','hosted-account-load-status');loadStatus.setAttribute('role','status');loadStatus.setAttribute('aria-live','polite');
  const retry=button('重试读取统计','hosted-quiet',()=>void load());retry.hidden=true;
  overview.append(element('span','hosted-section-kicker','工作台概览'),heading,intro,stats,loadStatus,retry);
  const lower=element('div','hosted-account-grid');
  const room=element('section','hosted-surface hosted-account-room');room.append(element('span','hosted-section-kicker','直播来源'));
  const roomHost=element('div','');const roomView=mountRoomControls(roomHost,api,presence);room.append(roomHost);
  const account=element('section','hosted-surface hosted-account-details');
  account.append(element('span','hosted-section-kicker','账号与协作'),element('h2','','主播账号'),element('p','hosted-copy','当前账号已登录。邀请其他主播使用在线服务，或将本地玩法配置迁移到这里。'));
  const invite=button('管理我的邀请码','hosted-primary',actions.onInvitations),migration=button('迁移本地配置','hosted-quiet',actions.onMigration);
  account.append(element('div','hosted-account-identity','已登录 · 在线服务'),invite,migration);
  lower.append(room,account);content.append(status,overview,lower);layout.append(sidebar,content);frame.append(header,layout);root.replaceChildren(frame);
  const load=async()=>{
    if(disposed||loading)return;loading=true;stats.setAttribute('aria-busy','true');retry.hidden=true;loadStatus.textContent='正在读取配置统计…';
    try {
      const config=await api.loadConfiguration();if(disposed)return;
      const list=(key:string)=>Array.isArray(config.definition[key])?config.definition[key] as unknown[]:[];
      const counts=[list('attributes').length,list('activities').length,list('giftTargetPanels').length,list('displayScenes').length];
      const details=[`${list('rules').length} 条礼物规则 · ${list('timerRules').length} 个定时规则`,'已配置的活动会话','已配置的礼物目标','已配置的组合画面'];
      metrics.forEach((metric,index)=>{metric.value.textContent=`${counts[index]} 个`;metric.description.textContent=details[index];});loadStatus.textContent='';
    } catch(error) {
      if(disposed)return;
      if(error instanceof HostedAPIError&&(error.status===401||error.status===403)){actions.onSignedOut();return;}
      loadStatus.textContent='暂时无法读取配置统计。请重试。';retry.hidden=false;
      metrics.forEach(metric=>{metric.value.textContent='—';metric.description.textContent='暂不可用';});
    } finally {if(!disposed){loading=false;stats.removeAttribute('aria-busy');}}
  };
  void load();
  return {dispose(){disposed=true;roomView.dispose();root.replaceChildren();}};
}
