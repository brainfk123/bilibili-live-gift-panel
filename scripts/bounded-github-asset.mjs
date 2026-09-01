import { createHash } from 'node:crypto';
import { writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const fail = () => { throw new Error('bounded GitHub asset download failed'); };

export async function downloadBoundedGitHubAsset(options) {
  let parsed;
  try { parsed = new URL(options.apiURL); } catch { fail(); }
  if (parsed.protocol !== 'https:' || parsed.hostname !== 'api.github.com' || !/^\/repos\/brainfk123\/bilibili-live-gift-panel\/releases\/assets\/[1-9][0-9]*$/.test(parsed.pathname) || parsed.search || parsed.hash ||
      !Number.isSafeInteger(options.expectedSize) || options.expectedSize <= 0 || options.expectedSize > options.maximumBytes ||
      !/^[0-9a-f]{64}$/.test(options.expectedSHA256) || !['application/json', 'application/octet-stream', 'text/plain'].includes(options.expectedContentType) || !options.token) fail();
  const fetchImpl = options.fetchImpl ?? fetch;
  let url = parsed.href;
  let authorized = true;
  let response;
  for (let redirect = 0; redirect <= 3; redirect += 1) {
    const headers = { Accept: 'application/octet-stream', 'X-GitHub-Api-Version': '2022-11-28' };
    if (authorized) headers.Authorization = `Bearer ${options.token}`;
    response = await fetchImpl(url, { method: 'GET', headers, redirect: 'manual' });
    if (![301, 302, 303, 307, 308].includes(response.status)) break;
    const location = response.headers.get('location');
    let next;
    try { next = new URL(location, url); } catch { fail(); }
    if (next.protocol !== 'https:' || !next.hostname.endsWith('.githubusercontent.com') || next.username || next.password) fail();
    url = next.href;
    authorized = false;
  }
  if (!response || response.status !== 200 || !response.body) fail();
  const contentType = (response.headers.get('content-type') || '').split(';', 1)[0].trim().toLowerCase();
  const contentLength = response.headers.get('content-length');
  if (contentType !== options.expectedContentType || (contentLength !== null && Number(contentLength) !== options.expectedSize)) fail();
  const chunks = []; let length = 0; const reader = response.body.getReader();
  for (;;) { const { done, value } = await reader.read(); if (done) break; length += value.byteLength; if (length > options.maximumBytes || length > options.expectedSize) { await reader.cancel(); fail(); } chunks.push(Buffer.from(value)); }
  const body = Buffer.concat(chunks, length);
  if (body.length !== options.expectedSize || createHash('sha256').update(body).digest('hex') !== options.expectedSHA256) fail();
  return body;
}

function argument(name) { const index=process.argv.indexOf(name); if(index<0||!process.argv[index+1])fail(); return process.argv[index+1]; }
async function main(){if(process.argv[2]!=='download')fail();const body=await downloadBoundedGitHubAsset({apiURL:argument('--api-url'),token:process.env.GH_TOKEN,expectedSize:Number(argument('--size')),expectedSHA256:argument('--sha256'),expectedContentType:argument('--content-type'),maximumBytes:Number(argument('--max-bytes'))});await writeFile(resolve(argument('--output')),body,{flag:'wx'});}
if(process.argv[1]&&import.meta.url===pathToFileURL(resolve(process.argv[1])).href){main().catch(()=>{console.error('bounded GitHub asset download failed');process.exitCode=1;});}
