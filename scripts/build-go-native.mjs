import { existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const nativeRoot = join(root, '.native', 'llama.cpp', 'windows-amd64');
const required = [
  join(nativeRoot, 'include', 'llama.h'),
  join(nativeRoot, 'lib', 'libllama.a'),
  join(nativeRoot, 'lib', 'ggml.a'),
  join(nativeRoot, 'lib', 'ggml-cpu.a'),
  join(nativeRoot, 'lib', 'ggml-base.a'),
];
for (const path of required) {
  if (!existsSync(path)) {
    throw new Error(`Missing native runtime artifact: ${path}. Run npm run build:llama first.`);
  }
}

process.env.CGO_ENABLED = '1';
process.env.GOOS = 'windows';
process.env.GOARCH = 'amd64';
process.env.CC ||= 'gcc';
process.env.CXX ||= 'g++';
process.env.GO_BUILD_TAGS = 'llamacpp';
process.env.GO_EXTERNAL_STATIC = '1';

await import('./build-go.mjs');
