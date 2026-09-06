export function downloadBoundedGitHubAsset(options: {
  apiURL: string;
  token: string;
  expectedSize: number;
  expectedSHA256: string;
  expectedContentType: 'application/json' | 'application/octet-stream' | 'text/plain';
  maximumBytes: number;
  fetchImpl?: typeof fetch;
}): Promise<Buffer>;
