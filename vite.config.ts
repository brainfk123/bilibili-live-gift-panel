import { defineConfig } from 'vitest/config';
import { viteSingleFile } from 'vite-plugin-singlefile';

export default defineConfig({
  plugins: [viteSingleFile({
    useRecommendedBuildConfig: false,
    // Keep configuration code as a separately served chunk. OBS starts only
    // from index.html, so it never receives configuration-only warnings.
    // Keep the entry and its shared dependency module as files. The lazy
    // configuration chunk imports that module, so removing it after inlining
    // would leave config mode with a dangling import in packaged output.
    inlinePattern: ['**/style-*.css'],
  })],
  base: './',
  build: {
    target: 'es2022',
    cssCodeSplit: false,
    rollupOptions: {
      preserveEntrySignatures: 'exports-only',
      output: {
        // Avoid Rollup's shared entry chunk: importing the config entry in a
        // browser then loads only its own module graph, never re-executes the
        // display page bootstrap. Every relative import remains a real file.
        preserveModules: true,
        preserveModulesRoot: 'src',
        entryFileNames: 'modules/[name]-[hash].js',
        chunkFileNames: 'modules/[name]-[hash].js',
      },
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
