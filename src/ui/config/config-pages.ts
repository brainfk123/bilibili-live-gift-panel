import type { ConfigPageId } from './config-route';

const PAGE_SECTION_SELECTORS: Record<ConfigPageId, string[]> = {
  overview: ['.connection-grid'],
  attributes: ['.attributes-section'],
  activities: ['.activity-workspace-section'],
  kpi: ['.gift-kpi-config-section'],
  obs: ['.display-scenes-section'],
  data: ['.contribution-section', '.gift-history-section'],
};

export function applyConfigPageVisibility(content: HTMLElement, activePage: ConfigPageId): void {
  content.dataset.activePage = activePage;
  for (const [page, selectors] of Object.entries(PAGE_SECTION_SELECTORS) as [ConfigPageId, string[]][]) {
    for (const selector of selectors) {
      for (const section of Array.from(content.querySelectorAll<HTMLElement>(selector))) {
        section.hidden = page !== activePage;
        section.setAttribute('aria-hidden', String(page !== activePage));
      }
    }
  }
}
