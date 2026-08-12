import { defineConfig } from 'vitest/config';
import { viteSingleFile } from 'vite-plugin-singlefile';

export default defineConfig({
  plugins: [viteSingleFile({
    useRecommendedBuildConfig: false,
    // Keep configuration code as a separately served chunk. OBS starts only
    // from index.html, so it never receives configuration-only warnings.
    inlinePattern: ['**/index-*.js', '**/style-*.css'],
  })],
  base: './',
  build: {
    target: 'es2022',
    cssCodeSplit: false,
    rollupOptions: {
      output: { chunkFileNames: 'chunks/[name]-[hash].js' },
    },
  },
  server: {
    proxy: {
      '/api': 'http://localhost:12450',
    },
  },
  test: {
    environment: 'node',
    include: ['tests/**/*.test.ts'],
  },
});
