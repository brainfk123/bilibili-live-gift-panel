import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  createReadStream,
  existsSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const LLAMA_CPP = Object.freeze({
  tag: 'b9637',
  commit: 'aedb2a5e9ca3d4064148bbb919e0ddc0c1b70ab3',
  archiveSha256: '762283319feb3de30886dc850d42f0e426b06600e7f9639d34e06506597309ca',
  archiveUrl: 'https://github.com/ggml-org/llama.cpp/archive/refs/tags/b9637.tar.gz',
});

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const nativeRoot = join(root, '.native', 'llama.cpp');
const archivePath = join(nativeRoot, `llama.cpp-${LLAMA_CPP.tag}.tar.gz`);
const sourceRoot = join(nativeRoot, 'source');
const sourceDir = join(sourceRoot, `llama.cpp-${LLAMA_CPP.tag}`);
const buildDir = join(nativeRoot, `build-${LLAMA_CPP.tag}-windows-amd64`);
const installDir = join(nativeRoot, 'windows-amd64');
const metadataPath = join(installDir, 'BUILD-METADATA.json');

if (process.platform !== 'win32' || process.arch !== 'x64') {
  throw new Error('The bundled llama.cpp runtime currently supports Windows x64 only.');
}

const cmake = process.env.CMAKE_BIN || 'cmake';
const ninja = process.env.NINJA_BIN || 'ninja';
const cc = process.env.CC || 'gcc';
const cxx = process.env.CXX || 'g++';

for (const [label, command, args] of [
  ['CMake', cmake, ['--version']],
  ['Ninja', ninja, ['--version']],
  ['MinGW C compiler', cc, ['--version']],
  ['MinGW C++ compiler', cxx, ['--version']],
]) {
  try {
    execFileSync(command, args, { stdio: 'ignore' });
  } catch {
    throw new Error(`${label} is required to build the local assistant runtime (${command}).`);
  }
}

mkdirSync(nativeRoot, { recursive: true });
await ensureArchive();
ensureSource();
applyMinGWCompatibilityPatch();

const desiredMetadata = {
  schemaVersion: 1,
  upstream: 'https://github.com/ggml-org/llama.cpp',
  tag: LLAMA_CPP.tag,
  commit: LLAMA_CPP.commit,
  archiveSha256: LLAMA_CPP.archiveSha256,
  target: 'windows-amd64',
  backend: 'cpu',
  sharedLibraries: false,
  nativeOptimizations: false,
  openmp: false,
  compatibilityPatch: 'mingw-thread-power-throttling-v1',
};

const existingMetadata = existsSync(metadataPath)
  ? JSON.parse(readFileSync(metadataPath, 'utf8'))
  : null;
const requiredLibraries = ['libllama.a', 'ggml.a', 'ggml-cpu.a', 'ggml-base.a'];
const installationCurrent =
  JSON.stringify(existingMetadata) === JSON.stringify(desiredMetadata) &&
  requiredLibraries.every((library) => existsSync(join(installDir, 'lib', library))) &&
  existsSync(join(installDir, 'include', 'llama.h'));

if (!installationCurrent) {
  removeBuildDirectory(buildDir);
  removeBuildDirectory(installDir);
  mkdirSync(buildDir, { recursive: true });
  mkdirSync(installDir, { recursive: true });

  const cmakePath = (value) => resolve(value).replaceAll('\\', '/');
  run(cmake, [
    '-S', cmakePath(sourceDir),
    '-B', cmakePath(buildDir),
    '-G', 'Ninja',
    `-DCMAKE_C_COMPILER=${cc}`,
    `-DCMAKE_CXX_COMPILER=${cxx}`,
    '-DCMAKE_BUILD_TYPE=Release',
    `-DCMAKE_INSTALL_PREFIX=${cmakePath(installDir)}`,
    '-DBUILD_SHARED_LIBS=OFF',
    '-DLLAMA_BUILD_COMMON=OFF',
    '-DLLAMA_BUILD_TESTS=OFF',
    '-DLLAMA_BUILD_TOOLS=OFF',
    '-DLLAMA_BUILD_EXAMPLES=OFF',
    '-DLLAMA_BUILD_SERVER=OFF',
    '-DLLAMA_BUILD_APP=OFF',
    '-DLLAMA_BUILD_UI=OFF',
    '-DGGML_BUILD_TESTS=OFF',
    '-DGGML_BUILD_EXAMPLES=OFF',
    '-DGGML_BACKEND_DL=OFF',
    '-DGGML_NATIVE=OFF',
    '-DGGML_OPENMP=OFF',
    '-DGGML_BLAS=OFF',
    '-DGGML_LLAMAFILE=OFF',
    '-DGGML_CUDA=OFF',
    '-DGGML_HIP=OFF',
    '-DGGML_VULKAN=OFF',
    '-DGGML_SYCL=OFF',
    '-DGGML_RPC=OFF',
    '-DGGML_CPU_ALL_VARIANTS=OFF',
  ]);
  run(cmake, ['--build', cmakePath(buildDir), '--config', 'Release']);
  run(cmake, ['--install', cmakePath(buildDir), '--config', 'Release']);

  for (const library of requiredLibraries) {
    if (!existsSync(join(installDir, 'lib', library))) {
      throw new Error(`llama.cpp static build did not install ${library}`);
    }
  }
  writeFileSync(metadataPath, `${JSON.stringify(desiredMetadata, null, 2)}\n`, 'utf8');
}

console.log(`llama.cpp ${LLAMA_CPP.tag} CPU runtime ready at ${installDir}`);

async function ensureArchive() {
  if (existsSync(archivePath) && (await sha256(archivePath)) === LLAMA_CPP.archiveSha256) {
    return;
  }
  const temporaryPath = `${archivePath}.partial`;
  const response = await fetch(LLAMA_CPP.archiveUrl, {
    headers: { 'User-Agent': 'bilibili-live-gift-panel-native-build' },
    redirect: 'follow',
  });
  if (!response.ok) {
    throw new Error(`Unable to download llama.cpp source: HTTP ${response.status}`);
  }
  const data = Buffer.from(await response.arrayBuffer());
  const digest = createHash('sha256').update(data).digest('hex');
  if (digest !== LLAMA_CPP.archiveSha256) {
    throw new Error(`llama.cpp source checksum mismatch: got ${digest}`);
  }
  writeFileSync(temporaryPath, data);
  renameSync(temporaryPath, archivePath);
}

function ensureSource() {
  const stampPath = join(sourceDir, '.blgp-source.json');
  if (existsSync(stampPath)) {
    const stamp = JSON.parse(readFileSync(stampPath, 'utf8'));
    if (stamp.commit === LLAMA_CPP.commit && stamp.archiveSha256 === LLAMA_CPP.archiveSha256) {
      return;
    }
  }
  removeBuildDirectory(sourceDir);
  mkdirSync(sourceRoot, { recursive: true });
  run(cmake, ['-E', 'tar', 'xzf', resolve(archivePath)], { cwd: sourceRoot });
  if (!existsSync(join(sourceDir, 'include', 'llama.h'))) {
    throw new Error('The verified llama.cpp archive did not contain the expected source tree.');
  }
  writeFileSync(stampPath, `${JSON.stringify(LLAMA_CPP, null, 2)}\n`, 'utf8');
}

function applyMinGWCompatibilityPatch() {
  const path = join(sourceDir, 'ggml', 'src', 'ggml-cpu', 'ggml-cpu.c');
  const marker = 'BLGP_MINGW_THREAD_POWER_THROTTLING_COMPAT';
  const source = readFileSync(path, 'utf8');
  if (source.includes(marker)) {
    return;
  }
  const needle = '#include <windows.h>\n\n#if defined(_MSC_VER) && !defined(__clang__)';
  if (!source.includes(needle)) {
    throw new Error('Unable to apply the pinned MinGW compatibility patch to ggml-cpu.c.');
  }
  const replacement = `#include <windows.h>

// BLGP_MINGW_THREAD_POWER_THROTTLING_COMPAT
// mingw-w64 runtime v11 exposes SetThreadInformation and ThreadPowerThrottling,
// but omits the matching structure and constants present in the Windows SDK.
#if defined(__MINGW32__) && !defined(THREAD_POWER_THROTTLING_CURRENT_VERSION)
#define THREAD_POWER_THROTTLING_CURRENT_VERSION 1
#define THREAD_POWER_THROTTLING_EXECUTION_SPEED 0x1
typedef struct _THREAD_POWER_THROTTLING_STATE {
    ULONG Version;
    ULONG ControlMask;
    ULONG StateMask;
} THREAD_POWER_THROTTLING_STATE, *PTHREAD_POWER_THROTTLING_STATE;
#endif

#if defined(_MSC_VER) && !defined(__clang__)`;
  writeFileSync(path, source.replace(needle, replacement), 'utf8');
}

function removeBuildDirectory(path) {
  const resolvedRoot = resolve(nativeRoot);
  const resolvedPath = resolve(path);
  const pathWithinRoot = relative(resolvedRoot, resolvedPath);
  if (!pathWithinRoot || pathWithinRoot.startsWith('..') || resolve(resolvedPath) === resolvedRoot) {
    throw new Error(`Refusing to remove unsafe native build path: ${resolvedPath}`);
  }
  rmSync(resolvedPath, { recursive: true, force: true });
}

function run(command, args, options = {}) {
  execFileSync(command, args, {
    cwd: options.cwd || root,
    stdio: 'inherit',
    env: process.env,
  });
}

function sha256(path) {
  return new Promise((resolveDigest, reject) => {
    const hash = createHash('sha256');
    const stream = createReadStream(path);
    stream.on('error', reject);
    stream.on('data', (chunk) => hash.update(chunk));
    stream.on('end', () => resolveDigest(hash.digest('hex')));
  });
}
