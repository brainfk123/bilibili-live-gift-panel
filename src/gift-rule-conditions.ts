export const GIFT_USER_IDENTITIES = [
  { value: 0, name: '普通用户' },
  { value: 1, name: '粉丝团' },
  { value: 2, name: '舰长' },
  { value: 3, name: '提督' },
  { value: 4, name: '总督' },
] as const;

export type GiftUserIdentity = (typeof GIFT_USER_IDENTITIES)[number]['value'];
export type QuickGiftConditionMode = 'any' | 'equal' | 'atLeast' | 'advanced';

export interface QuickGiftConditionDraft {
  mode: QuickGiftConditionMode;
  identity: Exclude<GiftUserIdentity, 0>;
}

const EQUAL = /^用户身份=(粉丝团|舰长|提督|总督)$/u;
const AT_LEAST = /^用户身份>=(粉丝团|舰长|提督|总督)$/u;
const ADVANCED_DRAFT: QuickGiftConditionDraft = { mode: 'advanced', identity: 2 };

export function buildQuickGiftCondition(
  mode: QuickGiftConditionMode,
  identity: Exclude<GiftUserIdentity, 0>,
): string | null {
  const name = GIFT_USER_IDENTITIES.find((candidate) => candidate.value === identity)?.name;
  if (!name) return null;
  if (mode === 'any') return '';
  if (mode === 'equal') return `用户身份=${name}`;
  if (mode === 'atLeast') return `用户身份>=${name}`;
  return null;
}

export function detectQuickGiftCondition(condition: string): QuickGiftConditionDraft {
  const normalized = condition.trim();
  const equal = normalized.match(EQUAL);
  if (equal) return { mode: 'equal', identity: identityForName(equal[1]) };
  const atLeast = normalized.match(AT_LEAST);
  if (atLeast) return { mode: 'atLeast', identity: identityForName(atLeast[1]) };
  return ADVANCED_DRAFT;
}

export function isGiftFormulaSystemName(name: string): boolean {
  return name === '用户身份' || GIFT_USER_IDENTITIES.some((identity) => identity.name === name);
}

function identityForName(name: string): Exclude<GiftUserIdentity, 0> {
  const identity = GIFT_USER_IDENTITIES.find((candidate) => candidate.name === name)?.value;
  return identity as Exclude<GiftUserIdentity, 0>;
}
