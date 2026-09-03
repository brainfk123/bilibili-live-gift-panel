import type { EVSignPublisherIdentity } from './sign-evsign.mjs';

export interface EnrollmentVerificationOptions {
  inspectorPath: string;
  artifactPath: string;
  artifactInspectionPath: string;
  artifactSidecarPath: string;
  standaloneFFmpegPath: string;
  ffmpegSidecarPath: string;
  ffmpegArchivePath: string;
  ffmpegManifestPath: string;
	expectedFFmpegManifestSHA256: string;
  rootSPKIPath: string;
  expectedRootSHA256: string;
  bootstrapPolicyPath: string;
  expectedBootstrapPolicySHA256: string;
  bootstrapPolicyEpoch: number;
  authorizationPolicyPath: string;
  expectedAuthorizationPolicySHA256: string;
  authorizationPolicyEpoch: number;
  version: string;
  tag: string;
  commit: string;
  outputPath: string;
  runInspector?: (arguments_: string[]) => string | Promise<string>;
}

export interface EnrollmentEvidence {
  schemaVersion: 1;
  version: string;
  tag: string;
  commit: string;
	artifact: { sha256: string; peContentSha256: string; signatureStatus: 'Valid'; identity: EVSignPublisherIdentity };
	root: { spkiSha256: string; rootKeyId: string };
  bootstrapPolicy: { sha256: string; epoch: number; signatureStatus: 'Valid' };
	authorizationPolicy: { sha256: string; epoch: number; signatureStatus: 'Valid'; scope: 'artifact-sha256' | 'publisher-identity'; tag: string; artifactSha256: string; identity: EVSignPublisherIdentity };
	ffmpeg: { version: string; sha256: string; archiveSha256: string; manifestSha256: string; signatureStatus: 'Valid'; identity: EVSignPublisherIdentity };
}

export function verifyEnrollmentBuild(options: EnrollmentVerificationOptions): Promise<EnrollmentEvidence>;
