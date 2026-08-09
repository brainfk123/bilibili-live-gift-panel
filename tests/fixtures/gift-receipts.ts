import configCSS from '../../src/ui/config/config.css?inline';
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

const state: AppState = {
  ...defaultState(),
  roomId: '31567150',
  settings: {
    ...defaultState().settings,
    showTutorial: false,
    tutorialReplayMode: false,
    tutorialCompletedLessons: ['room', 'attribute', 'template', 'basics', 'gift', 'rule', 'preset', 'timer', 'appearance', 'save', 'enable', 'output'],
  },
  giftCatalog: [
    { id: 32128, name: '心动盲盒', price: 960, coinType: 'gold', imgBasic: animationURL, gif: 'fixture.gif', animationDurationMs: 3000 },
    { id: 32126, name: '棉花糖', price: 540, coinType: 'gold', imgBasic: animationURL },
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
      senderUid: 2134957018, effects: [],
    },
    {
      id: 'animated-effect', time: Date.now(), giftId: 32128, giftName: '心动盲盒', num: 1,
      price: 960, totalCoin: 960, coinType: 'gold', uname: '鲑鱼盖饭', avatar: avatarURL,
      senderUid: 11578534, imgBasic: animationURL,
      animation: { gif: 'https://i0.hdslb.com/fixture.gif', webp: 'https://i0.hdslb.com/fixture.webp', durationMs: 3000 },
      effects: [{ attributeName: '挑战次数', delta: 960, valueAfter: 12745, ruleId: 'rule-1', triggerName: '心动盲盒规则' }],
    },
    {
      id: 'unmatched', time: Date.now() - 1000, giftId: 32126, giftName: '棉花糖', num: 2,
      price: 540, totalCoin: 1080, coinType: 'gold', uname: 'MarsOvO_w', avatar: avatarURL,
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
globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const requestURL = new URL(String(input), location.href);
  const method = init?.method ?? 'GET';
  if (requestURL.pathname === '/api/config' && method === 'GET') return json(state);
  if (requestURL.pathname === '/api/config') return json({ code: 0 });
  if (requestURL.pathname === '/api/runtime') return json({ code: 0, runtime: { state: 'connected', roomId: state.roomId } });
  if (requestURL.pathname === '/api/auth/status') return json({ code: 0, auth: { state: 'anonymous' } });
  if (requestURL.pathname === '/api/room/anchor') return json({ code: 0, roomId: state.roomId, anchor: { uid: 1, uname: '测试主播', avatar: avatarURL } });
  if (requestURL.pathname === '/api/gifts') return json({ code: 0, gifts: state.giftCatalog });
  if (requestURL.pathname === '/api/update') return json({ code: 0, update: { state: 'development', currentVersion: 'dev', message: '开发版本', autoUpdate: false, restartRequired: false } });
  if (requestURL.pathname === '/api/changelog') return json({ releases: [] });
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
