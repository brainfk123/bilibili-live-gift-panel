import configCSS from '../../src/ui/config/config.css?inline';
import { CHANGELOG_RELEASES } from '../../src/changelog';
import { defaultState, hydrateStateFromServer } from '../../src/storage';
import type { AppState } from '../../src/types';
import { mountConfig } from '../../src/ui/config/config';

const avatarSVG = `
<svg xmlns="http://www.w3.org/2000/svg" width="128" height="128" viewBox="0 0 128 128">
  <defs><linearGradient id="a" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#ff7aab"/><stop offset="1" stop-color="#6954ff"/></linearGradient></defs>
  <rect width="128" height="128" rx="64" fill="url(#a)"/>
  <circle cx="64" cy="50" r="24" fill="#fff4fa"/>
  <path d="M24 116c5-25 20-38 40-38s35 13 40 38" fill="#fff4fa"/>
</svg>`;

const animationSVG = `
<svg xmlns="http://www.w3.org/2000/svg" width="320" height="320" viewBox="0 0 320 320">
  <defs>
    <linearGradient id="box" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#ff9bc1"/><stop offset="1" stop-color="#ef4f91"/></linearGradient>
    <filter id="shadow"><feDropShadow dx="0" dy="12" stdDeviation="10" flood-opacity=".28"/></filter>
  </defs>
  <g filter="url(#shadow)">
    <animateTransform attributeName="transform" type="translate" values="0 8;0 -8;0 8" dur="1.2s" repeatCount="indefinite"/>
    <rect x="74" y="91" width="172" height="154" rx="28" fill="url(#box)" stroke="#ffd7e6" stroke-width="8"/>
    <path d="M160 91v154M74 140h172" stroke="#fff2f7" stroke-width="14"/>
    <path d="M160 91c-24-45-66-44-65-12 1 27 36 31 65 12zm0 0c24-45 66-44 65-12-1 27-36 31-65 12z" fill="#ffd45e"/>
  </g>
</svg>`;

const dataURL = (svg: string): string => `data:image/svg+xml,${encodeURIComponent(svg)}`;
const avatarURL = dataURL(avatarSVG);
const animationURL = dataURL(animationSVG);
const animatedGiftGIFURL = 'data:image/gif;base64,R0lGODlhQABAAIEAAAAAAP9dl//c6wAAACH/C05FVFNDQVBFMi4wAwEAAAAh+QQJFgAAACwAAAAAQABAAAAI1AABCBxIsKDBgwgTKlzIsKHDhxAjSpxIsaLFixgzatzIsaPHjyBDihxJsqTJkyhTqlzJsqXLlyQFyJw5E6ZBmjhl2hwoM4DPnz912uwJtGgAoS6JGi2KlKXSpUEFvHwK9ajUpAKqAm26kipUriq9LgWbUqxRsijNMr3aUu1Wtk6zavWJ9qTbqFPlzq1r8i5duF31auVb0q/VvHP/Ik5MOKbgqo1HGo4scjLgsI+/Xsa8d3NgyJ7j5qy5k+fo0KVTq17NurXr17Bjy55Nu7bt27hzlw4IACH5BAkWAAAALBgAEgARAB0AgQAAAP9dl//c6wAAAAhjAAEIBCCgoEGDAxMeXFgwIUEBASJKlNhQYMGJGCM2vJgx48GOHg2CxPhxJEWRJjWiTFmS5UqTLWG+HBmT5kyQNXHe7JiT586QHF0GlTnUZlGdR30+dDkwqUqHDBc6tBhVQMKAACH5BAkWAAAALCgAEgARAB0AgQAAAP9dl//c6wAAAAhjAAEIBCCgoEGDAxMeXFgwIUEBASJKlNhQYMGJGCM2vJgx48GOHg2CxPhxJEWRJjWiTFmS5UqTLWG+HBmT5kyQNXHe7JiT586QHF0GlTnUZlGdR30+dDkwqUqHDBc6tBhVQMKAADs=';
const changelogHistory = [
  ...CHANGELOG_RELEASES,
  {
    version: '0.2.4', date: '2026-08-08', title: '配置页面重新分区', summary: '按直播工作流程重新整理配置入口。',
    highlights: [{ label: '界面', title: '更清晰的页面导航', description: '不同用途的功能进入各自页面。' }],
    visuals: [],
  },
  {
    version: '0.2.3', date: '2026-08-07', title: '礼物目标与榜单', summary: '新增礼物目标并完善盲盒盈亏榜。',
    highlights: [{ label: '玩法', title: '设置礼物目标', description: '可为多种礼物分别设置目标数量。' }],
    visuals: [],
  },
  ...Array.from({ length: 11 }, (_, index) => ({
    version: `0.1.${10 - index}`,
    date: `2026-07-${String(31 - index).padStart(2, '0')}`,
    title: `历史版本 ${10 - index}`,
    summary: '用于验证联网加载大量历史版本时，版本列表和正文都能独立滚动。',
    highlights: [{ label: '历史', title: '较早版本内容', description: '更新日志仍然保持在弹窗可视范围内。' }],
    visuals: [],
  })),
];

const state: AppState = {
  ...defaultState(),
  roomId: '31567150',
  settings: {
    ...defaultState().settings,
    configExperience: 'advanced',
    showTutorial: false,
    tutorialReplayMode: false,
    tutorialCompletedLessons: ['room', 'attribute', 'template', 'basics', 'gift', 'rule', 'preset', 'timer', 'appearance', 'save', 'enable', 'output'],
  },
  giftCatalog: [
    { id: 32128, name: '心动盲盒', price: 960, coinType: 'gold', imgBasic: animationURL, gif: 'fixture.gif', animationDurationMs: 3000 },
    { id: 31164, name: '粉丝团灯牌', price: 100, coinType: 'gold', imgBasic: animationURL, gif: 'icon-only.gif', animationDurationMs: 3000 },
  ],
  contributions: {
    updatedAt: Date.now(),
    viewers: [
      {
        key: 'uid:11578534', uid: 11578534, uname: '鲑鱼盖饭', avatar: avatarURL,
        giftCount: 5, goldValue: 20_000, silverValue: 0, ruleTriggers: 3,
        attributeDeltas: { 挑战次数: 2_880 }, blindBoxCount: 2,
        blindBoxCost: 18_000, blindBoxValue: 24_000, blindBoxProfit: 6_000,
        lastGiftAt: Date.now(),
        blindBoxes: [{ giftId: 32128, giftName: '心动盲盒', count: 2, cost: 18_000, value: 24_000, profit: 6_000, lastGiftAt: Date.now() }],
      },
      {
        key: 'uid:274988853', uid: 274988853, uname: 'MarsOvO_w', avatar: avatarURL,
        giftCount: 2, goldValue: 10_000, silverValue: 0, ruleTriggers: 0,
        attributeDeltas: {}, blindBoxCount: 1,
        blindBoxCost: 9_000, blindBoxValue: 4_000, blindBoxProfit: -5_000,
        lastGiftAt: Date.now() - 1_000,
        blindBoxes: [{ giftId: 32128, giftName: '心动盲盒', count: 1, cost: 9_000, value: 4_000, profit: -5_000, lastGiftAt: Date.now() - 1_000 }],
      },
    ],
  },
  giftReceipts: [
    {
      id: 'super-chat', time: Date.now() + 1000, giftId: 1_900_000_004, giftName: 'Super Chat', num: 1,
      price: 50_000, totalCoin: 50_000, coinType: 'gold', uname: '送给暴脾气钢板', avatar: avatarURL,
      senderUid: 2134957018, membership: 'governor', message: '主播辛苦了，今天也要开心！', effects: [],
    },
    {
      id: 'animated-effect', time: Date.now(), giftId: 32128, giftName: '心动盲盒', num: 1,
      price: 960, totalCoin: 960, coinType: 'gold', uname: '鲑鱼盖饭', avatar: avatarURL,
      senderUid: 11578534, imgBasic: animationURL,
      membership: 'captain',
      animation: { gif: 'https://i0.hdslb.com/fixture.gif', webp: 'https://i0.hdslb.com/fixture.webp', durationMs: 3000 },
      effects: [{ attributeName: '挑战次数', delta: 960, valueAfter: 12745, ruleId: 'rule-1', triggerName: '心动盲盒规则' }],
    },
    {
      id: 'guard-effect', time: Date.now() - 500, giftId: 1_900_000_001, giftName: '大航海·舰长', num: 1,
      price: 198_000, totalCoin: 198_000, coinType: 'gold', uname: '新上舰观众', avatar: avatarURL,
      senderUid: 22334455, membership: 'admiral', imgBasic: animationURL,
      animation: { durationMs: 5000, effectId: 9001, mp4: 'https://i0.hdslb.com/guard.mp4', mp4Json: 'https://i0.hdslb.com/guard.json' },
      effects: [],
    },
    {
      id: 'unmatched', time: Date.now() - 1000, giftId: 31164, giftName: '粉丝团灯牌', num: 2,
      price: 100, totalCoin: 200, coinType: 'gold', uname: 'MarsOvO_w', avatar: avatarURL,
      senderUid: 274988853, imgBasic: animationURL, effects: [],
    },
    {
      id: 'animated-unmatched', time: Date.now() - 2000, giftId: 32128, giftName: '心动盲盒', num: 3,
      price: 960, totalCoin: 2880, coinType: 'gold', uname: '浅音初奈', avatar: avatarURL,
      senderUid: 3546594760723055, imgBasic: animationURL,
      animation: { gif: 'https://i0.hdslb.com/fixture.gif', durationMs: 1800 }, effects: [],
    },
  ],
};

const nativeImage = globalThis.Image;
class FixtureImage extends nativeImage {
  override set src(value: string) {
    if (value.includes('/api/gift-receipts/media') && value.includes('kind=animation')) super.src = animationURL;
    else if (value.includes('/api/gift-receipts/media') && value.includes('kind=avatar')) super.src = avatarURL;
    else super.src = value;
  }

  override get src(): string {
    return super.src;
  }
}
globalThis.Image = FixtureImage;

const json = (payload: unknown): Response => Response.json(payload);
const nativeFetch = globalThis.fetch.bind(globalThis);
globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const requestURL = new URL(String(input), location.href);
  const method = init?.method ?? 'GET';
  if (requestURL.pathname === '/api/gift-receipts/media' && requestURL.searchParams.get('kind') === 'animation') {
    return nativeFetch(animatedGiftGIFURL);
  }
  if (requestURL.pathname === '/api/config' && method === 'GET') return json(state);
  if (requestURL.pathname === '/api/config' && method === 'PATCH') {
    Object.assign(state, JSON.parse(String(init?.body ?? '{}')) as Partial<AppState>);
    return json({ code: 0 });
  }
  if (requestURL.pathname === '/api/config') return json({ code: 0 });
  if (requestURL.pathname === '/api/runtime') return json({ code: 0, runtime: { state: 'connected', roomId: state.roomId } });
  if (requestURL.pathname === '/api/auth/status') return json({ code: 0, auth: { state: 'anonymous' } });
  if (requestURL.pathname === '/api/room/anchor') return json({ code: 0, roomId: state.roomId, anchor: { uid: 1, uname: '测试主播', avatar: avatarURL } });
  if (requestURL.pathname === '/api/gifts') return json({ code: 0, gifts: state.giftCatalog });
  if (requestURL.pathname === '/api/update') return json({ code: 0, update: { state: 'development', currentVersion: 'dev', message: '开发版本', autoUpdate: false, restartRequired: false } });
  if (requestURL.pathname === '/api/changelog') return json({ code: 0, releases: changelogHistory });
  if (requestURL.pathname === '/api/gift-receipts' && method === 'DELETE') {
    state.giftReceipts = [];
    return json({ code: 0, giftReceipts: [] });
  }
  return json({ code: 0 });
};

const style = document.createElement('style');
style.textContent = configCSS;
document.head.append(style);
await hydrateStateFromServer();
const root = document.getElementById('app');
if (!root) throw new Error('fixture root missing');
mountConfig(root);
