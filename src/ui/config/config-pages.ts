import { CONFIG_PAGE_SECTION_SELECTORS, type ConfigPageId } from './config-route';

export function applyConfigPageVisibility(content: HTMLElement, activePage: ConfigPageId): void {
  content.dataset.activePage = activePage;
  for (const [page, selectors] of Object.entries(CONFIG_PAGE_SECTION_SELECTORS) as [ConfigPageId, readonly string[]][]) {
    for (const selector of selectors) {
      for (const section of Array.from(content.querySelectorAll<HTMLElement>(selector))) {
        section.hidden = page !== activePage;
        section.setAttribute('aria-hidden', String(page !== activePage));
      }
    }
  }
}
