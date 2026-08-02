export type DisplayFormat = 'hhmmss' | 'number' | 'suffix';

export interface Attribute {
  name: string;
  value: number;
  unit: 'seconds' | 'none';
  format: DisplayFormat;
  decimals: number;
  suffix: string;
  color?: string;
  broadcastMessage?: string;
}

export interface GiftRule {
  id: string;
  giftId: number;
  attributeName: string;
  formulaName?: string;
  formula: string;
  enabled?: boolean;
  /** The parent gift and exploded gifts matched by this rule. */
  matchGiftIds?: number[];
  minPrice?: number;
  cap?: number;
  dailyLimit?: number;
}

export interface TimerRule {
  id: string;
  attributeName: string;
  formulaName: string;
  intervalSeconds: number;
  condition?: string;
  formula: string;
  enabled: boolean;
}

export type FormulaPresetContext = 'gift' | 'timer';

export interface FormulaPreset {
  id: string;
  name: string;
  context: FormulaPresetContext;
  formula: string;
  sourceAttributeName: string;
}

export interface GiftInfo {
  id: number;
  name: string;
  price: number;
  coinType: 'gold' | 'silver';
  imgBasic: string;
}

export interface BlindBoxGift {
  id: number;
  name: string;
  price: number;
  imgBasic: string;
  chance?: string;
}

export interface BlindBoxInfo {
  giftId: number;
  name: string;
  gifts: BlindBoxGift[];
}

export interface RecentGift extends GiftInfo {
  lastReceived: number;
  count: number;
}

export interface DayStats {
  date: string;
  giftTotals: Record<number, number>;
  ruleTriggers: Record<string, number>;
}

export interface LogEntry {
  time: number;
  giftId: number;
  giftName: string;
  num: number;
  uname: string;
  avatar?: string;
  senderUid?: number;
  attributeName: string;
  delta: number;
  valueAfter: number;
  ruleId: string;
  source?: 'gift' | 'timer';
  triggerName?: string;
  eventId?: string;
}

export interface Settings {
  fontSize: number;
  accentColor: string;
  showStats: boolean;
  showConnection: boolean;
  align: 'left' | 'center' | 'right';
  theme: 'dark' | 'light';
  giftView: 'list' | 'grid';
  panelOpacity: number;
  showTutorial: boolean;
}

export interface AppState {
  roomId: string;
  attributes: Attribute[];
  rules: GiftRule[];
  timerRules: TimerRule[];
  formulaPresets: FormulaPreset[];
  settings: Settings;
  giftCatalog: GiftInfo[];
  recentGifts: RecentGift[];
  stats: Record<string, DayStats>;
  log: LogEntry[];
}

export const MAX_LOG = 200;
