import COS from 'cos-nodejs-sdk-v5';

const required = [
  'TENCENT_CLOUD_SECRET_ID',
  'TENCENT_CLOUD_SECRET_KEY',
  'COS_BUCKET',
  'COS_REGION',
];

for (const name of required) {
  if (!process.env[name]?.trim()) {
    throw new Error(`Missing required environment variable: ${name}`);
  }
}

const bucket = process.env.COS_BUCKET.trim();
const region = process.env.COS_REGION.trim();
const runId = process.env.GITHUB_RUN_ID || `local-${Date.now()}`;
const runAttempt = process.env.GITHUB_RUN_ATTEMPT || '1';
const key = `updates/connectivity-tests/run-${runId}-${runAttempt}.txt`;
const body = Buffer.from([
  'bilibili-live-gift-panel COS connectivity test',
  `run_id=${runId}`,
  `run_attempt=${runAttempt}`,
  `created_at=${new Date().toISOString()}`,
  '',
].join('\n'), 'utf8');

const cos = new COS({
  SecretId: process.env.TENCENT_CLOUD_SECRET_ID,
  SecretKey: process.env.TENCENT_CLOUD_SECRET_KEY,
});

function request(method, params) {
  return new Promise((resolve, reject) => {
    cos[method](params, (error, data) => {
      if (error) reject(error);
      else resolve(data);
    });
  });
}

function requestId(result) {
  return result?.headers?.['x-cos-request-id'] || 'unknown';
}

try {
  const uploaded = await request('putObject', {
    Bucket: bucket,
    Region: region,
    Key: key,
    Body: body,
    ContentLength: body.length,
    ContentType: 'text/plain; charset=utf-8',
  });

  const metadata = await request('headObject', {
    Bucket: bucket,
    Region: region,
    Key: key,
  });

  const remoteLength = Number(metadata?.headers?.['content-length']);
  if (remoteLength !== body.length) {
    throw new Error(`Uploaded object length mismatch: expected ${body.length}, got ${remoteLength}`);
  }

  const result = {
    ok: true,
    bucket,
    region,
    key,
    size: remoteLength,
    etag: metadata?.headers?.etag || uploaded?.ETag || null,
    put_request_id: requestId(uploaded),
    head_request_id: requestId(metadata),
  };

  console.log('COS connectivity test passed.');
  console.log(JSON.stringify(result, null, 2));

  if (process.env.GITHUB_STEP_SUMMARY) {
    const { appendFile } = await import('node:fs/promises');
    await appendFile(process.env.GITHUB_STEP_SUMMARY, [
      '## COS connectivity test passed',
      '',
      `- Bucket: \`${bucket}\``,
      `- Region: \`${region}\``,
      `- Object: \`${key}\``,
      `- Size: \`${remoteLength}\` bytes`,
      `- ETag: \`${result.etag || 'unknown'}\``,
      `- PUT Request ID: \`${result.put_request_id}\``,
      `- HEAD Request ID: \`${result.head_request_id}\``,
      '',
      '> The test object is intentionally retained because the uploader has no DeleteObject permission.',
      '',
    ].join('\n'));
  }
} catch (error) {
  const safeError = {
    name: error?.name,
    code: error?.code,
    statusCode: error?.statusCode,
    message: error?.message,
    requestId: error?.headers?.['x-cos-request-id'],
  };
  console.error('COS connectivity test failed.');
  console.error(JSON.stringify(safeError, null, 2));
  process.exitCode = 1;
}
