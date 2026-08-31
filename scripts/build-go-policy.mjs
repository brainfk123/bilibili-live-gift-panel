const ciWindowsSmokeProfile = 'ci-windows-smoke';

export function resolveBuildGoPolicy(environment = {}) {
  const rawAppVersion = environment.APP_VERSION || 'dev';
  const appVersion = rawAppVersion.replace(/^v/, '');
  const appCommit = environment.APP_COMMIT || 'local';
  const updateAPIURL = environment.APP_UPDATE_API_URL;
  const updatePublisher = (environment.APP_UPDATE_PUBLISHER || '').trim();
  const profile = environment.APP_BUILD_PROFILE || '';

  if (!profile) {
    return {
      profile: 'default',
      appVersion,
      appCommit,
      updateAPIURL,
      updatePublisher,
      requireAuthenticode: appVersion !== 'dev',
      verificationAppVersion: appVersion,
      verifyPayloadOnly: appVersion === 'dev',
    };
  }

  if (profile !== ciWindowsSmokeProfile) {
    throw new Error(`Unknown APP_BUILD_PROFILE: ${profile}`);
  }

  const hasExactSentinels = environment.CI === 'true'
    && environment.APP_VERSION === '0.0.0'
    && /^[0-9a-f]{40}$/i.test(environment.APP_COMMIT || '')
    && environment.APP_UPDATE_API_URL === 'https://updates.example.test'
    && environment.APP_UPDATE_PUBLISHER === 'CN=CI Smoke';
  if (!hasExactSentinels) {
    throw new Error('APP_BUILD_PROFILE=ci-windows-smoke requires exact CI smoke sentinel values');
  }

  return {
    profile: ciWindowsSmokeProfile,
    appVersion,
    appCommit,
    updateAPIURL,
    updatePublisher,
    requireAuthenticode: false,
    verificationAppVersion: 'dev',
    verifyPayloadOnly: true,
  };
}
