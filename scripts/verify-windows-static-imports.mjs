import { execFileSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { resolve } from 'node:path';

const executable = resolve(process.argv[2] || 'dist/gift-panel.exe');
if (!existsSync(executable)) {
  throw new Error(`Executable does not exist: ${executable}`);
}

const objdump = process.env.OBJDUMP_BIN || 'objdump';
const output = execFileSync(objdump, ['-p', executable], {
  encoding: 'utf8',
  maxBuffer: 16 * 1024 * 1024,
});
const imports = [...output.matchAll(/^\s*DLL Name:\s*(.+?)\s*$/gim)].map((match) => match[1]);
const forbidden = imports.filter((name) =>
  /^(libgcc_s|libstdc\+\+|libwinpthread|libgomp|libomp|llama|ggml).*\.dll$/i.test(name),
);
if (forbidden.length > 0) {
  throw new Error(`Executable depends on non-system native runtime DLLs: ${forbidden.join(', ')}`);
}
console.log(`verified static native runtime; imported DLLs: ${imports.join(', ')}`);
