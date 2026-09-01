export interface BridgeReadinessOptions {
  now: Date;
  stableReleaseBytes: Buffer;
  stableArtifactBytes: Buffer;
  stableChecksumBytes: Buffer;
  observationEvidenceBytes: Buffer;
  expectedObservationSHA256: string;
  rootSPKI: Buffer;
  bootstrapPolicyBytes: Buffer;
  expectedBootstrapPolicySHA256: string;
  bootstrapPolicyEpoch: number;
  authorizationPolicyBytes: Buffer;
  authorizationAuditBytes: Buffer;
  authorizationVerifiedBundleBytes: Buffer;
  authorizationPolicyReleaseBytes: Buffer;
  authorizationEvidenceBytes: Buffer;
  trustAttestationBytes: Buffer;
  expectedTrustAttestationSHA256: string;
}
export interface BridgeReadinessSummary {
  schemaVersion: 2;
  stableReleaseId: number;
  stablePublishedAt: string;
  stableArtifactSha256: string;
  observationEndedAt: string;
  observationEvidenceSha256: string;
  rootSpkiSha256: string;
  bootstrapPolicyEpoch: number;
  bootstrapPolicySha256: string;
  authorizationPolicyReleaseId: number;
  authorizationPolicyEpoch: number;
  authorizationPolicySha256: string;
  authorizationAuditSha256: string;
  authorizationCommitSha256: string;
  signerKeyId: string;
  signerRequestId: string;
  trustAttestationSha256: string;
}
export function verifyBridgeReadiness(options: BridgeReadinessOptions): BridgeReadinessSummary;
