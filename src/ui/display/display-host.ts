import type { ObsOutputTarget } from '../../obs-outputs';
import { mountBlindBoxDisplay } from './blind-box-display';
import { mountGiftKpiDisplay } from './gift-kpi-display';

type SpecializedDisplayTarget = Extract<ObsOutputTarget, { kind: 'blind-box' | 'gift-target' }>;
type SpecializedDisplayKind = SpecializedDisplayTarget['kind'];
type DisplayHost = (root: HTMLElement, target: SpecializedDisplayTarget) => void;

const DISPLAY_HOSTS: Record<SpecializedDisplayKind, DisplayHost> = {
  'blind-box': (root, target) => mountBlindBoxDisplay(
    root,
    target.kind === 'blind-box' ? target.blindBoxGiftId : undefined,
  ),
  'gift-target': (root, target) => mountGiftKpiDisplay(
    root,
    target.kind === 'gift-target' ? target.panelId : undefined,
  ),
};

export function mountSpecializedDisplay(root: HTMLElement, target?: ObsOutputTarget): boolean {
  if (!target || !(target.kind in DISPLAY_HOSTS)) return false;
  DISPLAY_HOSTS[target.kind as SpecializedDisplayKind](root, target as SpecializedDisplayTarget);
  return true;
}
