import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { mirrorUiAssets } from './ui-assets.mjs';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const manifest = mirrorUiAssets(join(root, 'dist'), join(root, 'goserver', 'dist'));
process.stdout.write(`Prepared ${manifest.files.length} embedded UI assets.\n`);
