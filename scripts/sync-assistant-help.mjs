import { copyFileSync, existsSync, mkdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const source = join(root, 'src', 'data', 'help-content.json');
const target = join(root, 'goserver', 'assistant', 'help-content.generated.json');
const checkOnly = process.argv.includes('--check');
const sourceBytes = readFileSync(source);
const targetCurrent = existsSync(target) && sourceBytes.equals(readFileSync(target));

if (checkOnly) {
  if (!targetCurrent) {
    throw new Error(
      'Generated assistant help content is stale. Run npm run sync:assistant-help and commit the result.',
    );
  }
  console.log('assistant help content is synchronized');
} else if (!targetCurrent) {
  mkdirSync(dirname(target), { recursive: true });
  copyFileSync(source, target);
  console.log(`synchronized ${source} -> ${target}`);
} else {
  console.log('assistant help content is already synchronized');
}
