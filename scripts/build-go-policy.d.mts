export interface BuildGoPolicy {
  profile: 'default' | 'ci-windows-smoke';
  appVersion: string;
  appCommit: string;
  updateAPIURL: string | undefined;
  updatePublisher: string;
  requireAuthenticode: boolean;
  verificationAppVersion: string;
  verifyPayloadOnly: boolean;
}

export function resolveBuildGoPolicy(
  environment?: Readonly<Record<string, string | undefined>>,
): BuildGoPolicy;
