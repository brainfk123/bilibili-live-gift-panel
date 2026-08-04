import type { TrainingTopicId, TutorialLesson } from './types';

export type TrainingTopicCategory = 'advanced' | 'troubleshooting';
export type TrainingCatalogCategory = 'all' | 'main' | TrainingTopicCategory;
export type TrainingEditorSection = 'overview' | 'rules' | 'timers' | 'output';

export type TrainingDestination =
  | { kind: 'editor'; section: TrainingEditorSection }
  | { kind: 'page'; selector: string };

export interface TrainingTopicDefinition {
  id: TrainingTopicId;
  category: TrainingTopicCategory;
  title: string;
  summary: string;
  keywords: string[];
  steps: string[];
  outcome: string;
  destination: TrainingDestination;
  actionLabel: string;
  requiresAttribute?: boolean;
}

export interface MainLessonDetail {
  steps: string[];
  outcome: string;
}

export const MAIN_LESSON_DETAILS: Record<TutorialLesson, MainLessonDetail> = {
  room: {
    steps: ['找到直播间网址末尾的数字。', '填写房间号并点击“测试连接”。', '看到“已连接”后再继续。'],
    outcome: '托盘后台知道该监听哪个直播间，配置页关闭后也能继续接收礼物。',
  },
  attribute: {
    steps: ['点击“添加属性”。', '进入同一页面中的属性工作台。', '之后的每项设置都会归到这个属性下。'],
    outcome: '属性是后台保存和修改的一份数据，例如加班时间、血量或票数。',
  },
  basics: {
    steps: ['套用加班机模板。', '确认名称、起始值和显示格式。', '观察右侧数值预览。'],
    outcome: '名称和值参与后台计算；显示格式只改变 OBS 里的呈现。',
  },
  gift: {
    steps: ['打开礼物选择器。', '选择一个直播间可赠送的礼物。', '点击“确认选择”返回规则区。'],
    outcome: '只有被选中的礼物才会绑定到当前属性；之后还可以继续添加任意数量。',
  },
  rule: {
    steps: ['选择“增加”等小白规则。', '填写每次变化量。', '点击模拟，确认前值、后值和变化量。'],
    outcome: '每收到单个礼物，后台独立执行一次规则；模拟不会修改直播中的真实数值。',
  },
  timer: {
    steps: ['添加一个定时器。', '设置间隔、条件和每次变化。', '模拟一次并观察条件是否满足。'],
    outcome: '定时器由托盘后台运行，不需要 OBS 页面保持打开，也不会显示为礼物卡片。',
  },
  preset: {
    steps: ['在高级规则中点击“保存预设”。', '给当前计算方法起一个容易识别的名字。', '以后给其他礼物配置时直接套用。'],
    outcome: '预设只保存计算方法，不会复制礼物、属性当前值或启用状态。',
  },
  save: {
    steps: ['检查各工作区的数量提示。', '点击“创建属性”或“保存修改”。', '等待后台校验完成。'],
    outcome: '只有合法配置才会写入本机；出错时会指出需要修改的区域。',
  },
  enable: {
    steps: ['回到属性卡片。', '打开礼物规则右侧的开关。', '确认卡片不再是灰色。'],
    outcome: '关闭的规则会保留配置，但后台不会执行，也不会出现在 OBS 规则卡片中。',
  },
  output: {
    steps: ['预览当前属性面板。', '复制这个属性的专属 OBS 链接。', '在 OBS 中添加“浏览器”来源。'],
    outcome: '配置页负责设置，托盘后台负责收礼和计算，OBS 链接只负责显示；配置页可以关闭。',
  },
};

export const TRAINING_TOPICS: TrainingTopicDefinition[] = [
  {
    id: 'multi-gift',
    category: 'advanced',
    title: '多个礼物影响同一属性',
    summary: '给一个属性绑定多个礼物，并让每种礼物使用自己的规则。',
    keywords: ['多个礼物', '任意数量', '礼物规则', '绑定'],
    steps: ['进入属性的“礼物规则”工作区。', '点击“添加礼物”并一次选择多个礼物。', '分别配置、模拟并启用每张规则卡片。'],
    outcome: '同一次送礼只会匹配对应礼物的已启用规则，不会让无关礼物改变数值。',
    destination: { kind: 'editor', section: 'rules' },
    actionLabel: '打开礼物规则',
    requiresAttribute: true,
  },
  {
    id: 'blind-box',
    category: 'advanced',
    title: '正确处理盲盒礼物',
    summary: '把父盲盒自动关联到可能开出的实际礼物，避免漏记。',
    keywords: ['盲盒', '父礼物', '实际礼物', '登录', '背包'],
    steps: ['可选登录任意普通 B 站账号，以读取盲盒内容。', '选择父盲盒，等待出现已识别提示。', '模拟并保存；后台会按实际开出的礼物匹配父盲盒规则。'],
    outcome: '登录可自动补全盲盒内容；无法识别时仍保留实际礼物记录，不会猜测错误映射。',
    destination: { kind: 'editor', section: 'rules' },
    actionLabel: '打开礼物规则',
    requiresAttribute: true,
  },
  {
    id: 'manual-gift',
    category: 'advanced',
    title: '按 ID 添加搜索不到的礼物',
    summary: '活动或历史礼物不在默认列表时，使用完整数字 ID 精确添加。',
    keywords: ['礼物ID', '搜索不到', '活动礼物', '历史礼物', '手动添加'],
    steps: ['在礼物选择器中先搜索完整数字 ID。', '仍找不到时展开“按 ID 手动添加”。', '填写名称和单价后确认选择，再用真实记录核对。'],
    outcome: '数字 ID 必须完整匹配；手动条目不会被误认为直播间当前已上架礼物。',
    destination: { kind: 'editor', section: 'rules' },
    actionLabel: '打开礼物选择器',
    requiresAttribute: true,
  },
  {
    id: 'advanced-rule',
    category: 'advanced',
    title: '高级规则：条件、上下限与随机',
    summary: '使用 IF、MIN、MAX 和 RANDBETWEEN 表达更复杂的变化。',
    keywords: ['IF', 'MIN', 'MAX', 'RANDBETWEEN', '随机', '上限', '下限', '条件'],
    steps: ['先用小白模式得到一个可工作的基础规则。', '展开“高级规则”查看等价表达式。', '逐项加入条件或上下限，每次修改后都先模拟。'],
    outcome: '高级表达式仍以等号右侧的结果作为属性新值；错误不会写入正式配置。',
    destination: { kind: 'editor', section: 'rules' },
    actionLabel: '打开高级规则',
    requiresAttribute: true,
  },
  {
    id: 'cross-attribute',
    category: 'advanced',
    title: '让多个属性互相联动',
    summary: '在一条规则中读取其他属性的当前值，制作资源交换或连锁玩法。',
    keywords: ['跨属性', '联动', '引用', '多个属性', '资源交换'],
    steps: ['先创建并保存被引用的另一个属性。', '在高级规则中直接写入那个属性的完整名称。', '用模拟预览检查当前值参与计算后的结果。'],
    outcome: '规则只能写回它所属的属性，但可以读取其他已存在属性的当前值。',
    destination: { kind: 'editor', section: 'rules' },
    actionLabel: '打开高级规则',
    requiresAttribute: true,
  },
  {
    id: 'display-format',
    category: 'advanced',
    title: '数字、计时器与枚举显示',
    summary: '同一份后台数值可以显示为时间、带单位数字或图文状态。',
    keywords: ['显示格式', '计时器', '后缀', '枚举', '图标', '状态'],
    steps: ['在“概览”选择基础数值格式。', '需要状态文字时启用枚举展示并配置数值映射。', '在输出预览中检查字号、颜色和长文本。'],
    outcome: '显示设置不会改计算结果；枚举只把指定数值映射成文字、颜色或图片。',
    destination: { kind: 'editor', section: 'overview' },
    actionLabel: '打开显示设置',
    requiresAttribute: true,
  },
  {
    id: 'broadcast-output',
    category: 'advanced',
    title: '默认消息与礼物播报',
    summary: '设置空闲时循环的文字，并理解高频送礼如何排队播报。',
    keywords: ['播报', '默认消息', '送礼消息', '队列', '头像', '昵称'],
    steps: ['在“输出与预览”填写默认播报消息。', '检查礼物规则卡片和播报条的布局。', '实际送礼时，后台会把每条有效消息依次加入播报队列。'],
    outcome: '默认消息只在没有礼物播报时显示；礼物播报不会替代规则卡片。',
    destination: { kind: 'editor', section: 'output' },
    actionLabel: '打开输出设置',
    requiresAttribute: true,
  },
  {
    id: 'combined-scenes',
    category: 'advanced',
    title: '一个 OBS 链接显示多个属性',
    summary: '创建组合面板，选择多个属性并安排堆叠或网格布局。',
    keywords: ['组合面板', '多个属性', 'OBS链接', '网格', '堆叠'],
    steps: ['先创建至少两个属性。', '在配置页新建组合面板并选择属性顺序。', '复制组合面板链接到一个 OBS 浏览器来源。'],
    outcome: '单属性链接仍只显示一个属性；组合面板使用独立链接并共享统一主题。',
    destination: { kind: 'page', selector: '.display-scenes-section' },
    actionLabel: '查看组合面板',
  },
  {
    id: 'activity-session',
    category: 'advanced',
    title: '控制一局互动的开始与结算',
    summary: '用活动会话管理准备、进行、锁定和结算阶段。',
    keywords: ['活动会话', '开始', '结算', '锁定', '里程碑', '对战', '票选'],
    steps: ['选择活动使用的属性和组合面板。', '按需开启“仅活动进行时响应规则”。', '设置里程碑后开始活动，结束时锁定并结算。'],
    outcome: '活动会话只控制本局状态；属性、礼物规则和组合面板仍是可复用配置。',
    destination: { kind: 'page', selector: '.activity-workspace-section' },
    actionLabel: '查看活动会话',
  },
  {
    id: 'contribution-ranking',
    category: 'advanced',
    title: '核对贡献与盲盒盈亏',
    summary: '查看所有送礼、真实规则命中和盲盒实际价值的后台统计。',
    keywords: ['贡献', '排行榜', '盲盒盈亏', '规则命中', '送礼记录'],
    steps: ['切换礼物贡献、规则命中和盲盒盈亏三个榜单。', '核对观众、礼物价值和属性净变化。', '需要重新统计时只清空排行榜，不会修改属性。'],
    outcome: '榜单由后台收到的真实事件累计；未命中的礼物不会计入规则命中和属性变化。',
    destination: { kind: 'page', selector: '.contribution-section' },
    actionLabel: '查看贡献排行',
  },
  {
    id: 'rule-no-effect',
    category: 'troubleshooting',
    title: '礼物到了但数值没变化',
    summary: '按顺序检查礼物 ID、启用开关、条件、活动状态和后台记录。',
    keywords: ['没触发', '没变化', '礼物规则', '启用', '礼物ID', '活动'],
    steps: ['先在生效记录中确认这条礼物是否匹配到规则。', '检查属性卡片上的规则开关是否打开。', '再检查高级条件、盲盒映射和活动会话是否允许执行。'],
    outcome: '模拟成功只证明表达式有效；正式执行还需要礼物匹配、规则启用和活动状态同时满足。',
    destination: { kind: 'editor', section: 'rules' },
    actionLabel: '检查礼物规则',
    requiresAttribute: true,
  },
  {
    id: 'timer-skipped',
    category: 'troubleshooting',
    title: '定时器为什么没有运行',
    summary: '检查外部开关、完整间隔、运行条件和下限保护。',
    keywords: ['定时器', '跳过', '没运行', '条件', '间隔', '关闭后开启'],
    steps: ['确认属性卡片上的定时器开关已打开。', '等待一个完整触发间隔；重新启用会从新间隔开始。', '模拟当前值，查看条件不满足还是结果没有变化。'],
    outcome: '定时器由后台独立运行；OBS 和配置页面是否打开都不会影响它。',
    destination: { kind: 'editor', section: 'timers' },
    actionLabel: '检查定时器',
    requiresAttribute: true,
  },
  {
    id: 'obs-no-change',
    category: 'troubleshooting',
    title: 'OBS 面板没有更新',
    summary: '区分后台没有计算、复制错链接和 OBS 浏览器缓存三类问题。',
    keywords: ['OBS', '不更新', '没更新', '没有变化', '浏览器来源', '缓存', '链接'],
    steps: ['先在配置页确认属性当前值已经变化。', '核对 OBS 使用的是当前属性或组合面板的专属链接。', '值正确但画面旧时，在 OBS 中刷新浏览器来源。'],
    outcome: 'OBS 只显示后台状态；它不负责连接直播间，也不执行礼物规则或定时器。',
    destination: { kind: 'editor', section: 'output' },
    actionLabel: '检查输出预览',
    requiresAttribute: true,
  },
];

const TOPIC_IDS = new Set<TrainingTopicId>(TRAINING_TOPICS.map((topic) => topic.id));

export function normalizeTrainingTopicIds(value: unknown): TrainingTopicId[] {
  if (!Array.isArray(value)) return [];
  return Array.from(new Set(value.filter((item): item is TrainingTopicId => (
    typeof item === 'string' && TOPIC_IDS.has(item as TrainingTopicId)
  ))));
}

export function matchesTrainingTopic(topic: TrainingTopicDefinition, query: string): boolean {
  return matchesTrainingText(
    [topic.title, topic.summary, topic.outcome, ...topic.keywords, ...topic.steps],
    query,
  );
}

export function matchesTrainingText(values: string[], query: string): boolean {
  const tokens = query
    .trim()
    .toLocaleLowerCase('zh-CN')
    .split(/\s+/)
    .filter(Boolean);
  if (tokens.length === 0) return true;
  const haystack = values.join('\n').toLocaleLowerCase('zh-CN');
  return tokens.every((token) => haystack.includes(token));
}
