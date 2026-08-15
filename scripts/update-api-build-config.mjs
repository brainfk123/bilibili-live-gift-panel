import { isIP } from 'node:net';

export function resolveUpdateAPIBaseURLHex(appVersion, configuredValue) {
  const updateAPIBaseURL = (configuredValue || '').trim();
  if (!updateAPIBaseURL) {
    if (appVersion !== 'dev') throw new Error('Release build requires APP_UPDATE_API_URL.');
    return '';
  }

  let parsed;
  try {
    parsed = new URL(updateAPIBaseURL);
  } catch (error) {
    throw new Error(`APP_UPDATE_API_URL must be an absolute URL: ${error.message}`);
  }
  if (
    parsed.protocol !== 'https:'
    || !parsed.hostname
    || parsed.username
    || parsed.password
    || parsed.search
    || parsed.hash
    || parsed.pathname !== '/'
    || (parsed.port && (!/^[1-9][0-9]*$/.test(parsed.port) || Number(parsed.port) > 65535))
    || !/^[\x00-\x7F]+$/.test(parsed.hostname)
    || isIP(parsed.hostname.replace(/^\[|\]$/g, '')) !== 0
    || (updateAPIBaseURL !== parsed.origin && updateAPIBaseURL !== `${parsed.origin}/`)
  ) {
    throw new Error('APP_UPDATE_API_URL must be a canonical ASCII HTTPS origin without credentials, query, fragment, or path.');
  }
  return Buffer.from(parsed.origin, 'utf8').toString('hex');
}
