export type DisplayFormat = 'hhmmss' | 'number' | 'suffix';

export type DisplayThemeId = 'minimal' | 'glass' | 'rpg' | 'pixel' | 'neon' | 'kawaii';

export type DisplayVariant = 'number' | 'timer' | 'progress' | 'health' | 'resource' | 'tug' | 'enum';

export type DisplaySceneLayout = 'stack' | 'grid';

export interface DisplayScene {
  id: string;
  name: string;
  attributeNames: string[];
  layout: DisplaySceneLayout;
  themeId: DisplayThemeId;
}

export type ActivityStatus = 'not_started' | 'active' | 'locked' | 'settled';

export type ActivityResultMode = 'none' | 'highest' | 'lowest';

export interface ActivityResult {
  winnerAttributeName?: string;
  values: Record<string, number>;
}

export type ActivityMilestoneComparison = 'gte' | 'lte';

export type ActivityMilestoneAction = 'announce' | 'lock' | 'settle';

export interface ActivityMilestone {
  id: string;
  name: string;
  attributeName: string;
  comparison: ActivityMilestoneComparison;
  threshold: number;
  action: ActivityMilestoneAction;
  message: string;
  triggeredAt?: number;
  triggerValue?: number;
}

export type ActivityGiftTimeoutAction = 'lock' | 'settle' | 'reset';

export interface ActivityGiftTimeout {
  seconds: number;
  action: ActivityGiftTimeoutAction;
  lastGiftAt?: number;
  deadlineAt?: number;
}

export interface ActivitySession {
  id: string;
  name: string;
  attributeNames: string[];
  sceneId?: string;
  status: ActivityStatus;
  resultMode: ActivityResultMode;
  gateRules: boolean;
  initialValues: Record<string, number>;
  milestones: ActivityMilestone[];
  giftTimeout?: ActivityGiftTimeout;
  startedAt?: number;
  lockedAt?: number;
  settledAt?: number;
  result?: ActivityResult;
}

export interface AttributeDisplay {
  variant: DisplayVariant;
  themeId?: DisplayThemeId;
  title?: string;
  min?: number;
  max?: number;
  lowThreshold?: number;
  leftLabel?: string;
  rightLabel?: string;
  valueMappings?: AttributeValueMapping[];
}

export interface AttributeValueMapping {
  value: number;
  label: string;
  color?: string;
  imageUrl?: string;
}

export interface Attribute {
  id?: string;
  name: string;
  value: number;
  unit: 'seconds' | 'none';
  format: DisplayFormat;
  decimals: number;
  suffix: string;
  color?: string;
  broadcastMessage?: string;
  display?: AttributeDisplay;
  createdFromTemplateId?: string;
  createdFromTemplateVersion?: number;
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

export type TutorialLesson =
  | 'room'
  | 'attribute'
  | 'template'
  | 'basics'
  | 'gift'
  | 'rule'
  | 'preset'
  | 'timer'
  | 'appearance'
  | 'save'
  | 'enable'
  | 'output';

export type TrainingTopicId =
  | 'multi-gift'
  | 'blind-box'
  | 'manual-gift'
  | 'advanced-rule'
  | 'cross-attribute'
  | 'display-format'
  | 'broadcast-output'
  | 'combined-scenes'
  | 'activity-session'
  | 'contribution-ranking'
  | 'rule-no-effect'
  | 'timer-skipped'
  | 'obs-no-change';

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
  /** Whether the gift is currently listed in the connected room. */
  listed?: boolean;
  /** Stable synthetic event used to configure non-SEND_GIFT paid live events. */
  specialEvent?: 'guard-captain' | 'guard-admiral' | 'guard-governor' | 'super-chat';
  /** Parent blind-box metadata used when SEND_GIFT only contains the opened reward. */
  blindBoxParentId?: number;
  blindBoxParentName?: string;
  blindBoxParentPrice?: number;
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

export interface ViewerContribution {
  key: string;
  uid?: number;
  uname: string;
  avatar?: string;
  giftCount: number;
  goldValue: number;
  silverValue: number;
  ruleTriggers: number;
  attributeDeltas: Record<string, number>;
  blindBoxCount: number;
  blindBoxCost: number;
  blindBoxValue: number;
  blindBoxProfit: number;
  unpricedBlindBoxCount?: number;
  lastGiftAt: number;
}

export interface ContributionLedger {
  viewers: ViewerContribution[];
  updatedAt?: number;
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
  defaultDisplayThemeId: DisplayThemeId;
  showTutorial: boolean;
  tutorialVersion: number;
  tutorialCompletedLessons: TutorialLesson[];
  tutorialReplayMode: boolean;
  tutorialTargetAttributeId?: string;
  trainingCompletedTopics: TrainingTopicId[];
  lastSeenChangelogVersion: string;
  autoUpdate: boolean;
}

export interface AppState {
  roomId: string;
  attributes: Attribute[];
  displayScenes: DisplayScene[];
  activities: ActivitySession[];
  rules: GiftRule[];
  timerRules: TimerRule[];
  formulaPresets: FormulaPreset[];
  settings: Settings;
  giftCatalog: GiftInfo[];
  recentGifts: RecentGift[];
  stats: Record<string, DayStats>;
  log: LogEntry[];
  contributions: ContributionLedger;
}

export const MAX_LOG = 200;
