export interface UIParityViewport {
  id: 'desktop-1440x900' | 'narrow-1024x768' | 'mobile-390x844';
  width: number;
  height: number;
  deviceScaleFactor: 1;
}
export interface UIParityFeature {
  id: 'overview' | 'attributes' | 'activities' | 'gift-targets' | 'obs' | 'analytics';
  states: string[];
  interactions: string[];
  compare: ('structure' | 'hierarchy' | 'spacing' | 'controls' | 'states' | 'responsive' | 'interactions')[];
}
export interface UIParityRequirements {
  schema: 1;
  reference: { product: 'gift-panel-exe'; minimumVersion: '0.4.10' };
  viewports: UIParityViewport[];
  features: UIParityFeature[];
}
export function validateUIParityRequirements(value: unknown): UIParityRequirements;
