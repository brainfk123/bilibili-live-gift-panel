import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';

const projectRoot = fileURLToPath(new URL('.', import.meta.url));

export default defineConfig({
  base: '/',
  build: {
    target: 'es2022',
    outDir: resolve(projectRoot, 'goserver/cmd/hosted/dist'),
    emptyOutDir: true,
    manifest: true,
    rollupOptions: {
      input: {
        hosted: resolve(projectRoot, 'hosted.html'),
        obs: resolve(projectRoot, 'obs.html'),
      },
    },
  },
});
