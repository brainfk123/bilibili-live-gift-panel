import type { GiftKpiItem, GiftKpiPanel } from './types';

export type GiftTargetItemConfig = Omit<GiftKpiItem, 'received'>;
export type GiftTargetPanelConfig = Omit<GiftKpiPanel, 'items'> & { items: GiftTargetItemConfig[] };

export interface GiftTargetProgressSnapshot {
  panelId: string;
  items: Array<{ giftId: number; received: number }>;
}

export function giftTargetPanelConfig(panel: GiftKpiPanel): GiftTargetPanelConfig {
  return {
    ...panel,
    items: panel.items.map(({ received: _received, ...item }) => item),
  };
}

export function mergeGiftTargetPanelConfigs(
  current: readonly GiftKpiPanel[],
  configured: readonly GiftTargetPanelConfig[],
): GiftKpiPanel[] {
  const progressByPanel = new Map(current.map((panel) => [
    panel.id,
    new Map(panel.items.map((item) => [item.giftId, item.received])),
  ]));
  return configured.map((panel) => ({
    ...panel,
    items: panel.items.map((item) => ({
      ...item,
      received: progressByPanel.get(panel.id)?.get(item.giftId) ?? 0,
    })),
  }));
}

export function applyGiftTargetProgressSnapshot(panel: GiftKpiPanel, snapshot: GiftTargetProgressSnapshot): void {
  if (panel.id !== snapshot.panelId) return;
  const receivedByGift = new Map(snapshot.items.map((item) => [item.giftId, item.received]));
  for (const item of panel.items) item.received = Math.max(0, receivedByGift.get(item.giftId) ?? 0);
}

export function giftTargetProgressSignature(panels: readonly GiftKpiPanel[]): string {
  return JSON.stringify(panels.map((panel) => [panel.id, panel.items.map((item) => [item.giftId, item.received])]));
}
