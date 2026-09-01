export interface BridgeReadinessOptions {
  now: Date;
  stableReleaseBytes: Buffer;
  stableArtifactBytes: Buffer;
  stableChecksumBytes: Buffer;
  observationEvidenceBytes: Buffer;
  expectedObservationSHA256: string;
  rootSPKI: Buffer;
  policyBytes: Buffer;
  auditBytes: Buffer;
  verifiedBundleBytes: Buffer;
  policyReleaseBytes: Buffer;
  trustAttestationBytes: Buffer;
  expectedTrustAttestationSHA256: string;
}
export interface BridgeReadinessSummary {
  schemaVersion: 1;
  stableReleaseId: number;
  stablePublishedAt: string;
  stableArtifactSha256: string;
  observationEndedAt: string;
  observationEvidenceSha256: string;
  policyReleaseId: number;
  policyEpoch: 1;
  policySha256: string;
  rootSpkiSha256: string;
  kmsKeyId: string;
  kmsRequestId: string;
  trustAttestationSha256: string;
}
export function verifyBridgeReadiness(options: BridgeReadinessOptions): BridgeReadinessSummary;
