import configStyles from './config.css?inline';
import { mountConfig } from './config';

export function mountConfigEntry(root: HTMLElement): void {
  mountConfig(root);
}

export { configStyles };
