export interface PublisherChangeIdentity {
  country: string;
  organization: string;
  organizationId: string;
}

export interface PublisherChangeRequestInput {
  tag: string;
  artifactSha256: string;
  certificateDerSha256: string;
  identity: PublisherChangeIdentity;
  currentPolicyEpoch: number;
  runId: string;
  runAttempt: number;
}

export function buildPublisherChangeRequest(input: PublisherChangeRequestInput): Buffer;
export function writePublisherChangeRequest(path: string, input: PublisherChangeRequestInput): Promise<void>;
