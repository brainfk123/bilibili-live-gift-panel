# Version-Aware Signer Trust Rotation Design

## Status

Approved design only. This document does not authorize implementation, KMS provisioning, credential creation, server changes, COS writes, Git pushes, tags, or releases.

## Goal

Allow supported Gift Panel clients to keep updating safely when the signing service renews a certificate for the same legal publisher, and to migrate automatically across publisher legal entities only after an independent Tencent Cloud KMS trust root authorizes that transition.

The immediate migration must recover v0.4.7 clients that trust RushRush, preserve v0.4.9 and v0.4.10 clients that trust NaisNet, and converge both populations back to one NaisNet stable channel without mutating any existing release.

## Verified production facts

As of 2026-08-30:

- v0.4.7 is signed by `RushRush Network Technology Ltd`, organization identifier `91450900MADM3GLG5P`.
- v0.4.10 is signed by `NaisNet Technology Co., Ltd.`, organization identifier `91210103MA7CJ3C094`.
- Both signatures are Windows-valid and chain through `Certum Extended Validation Code Signing 2021 CA` to the public root `Certum Trusted Network CA 2`, thumbprint `D3DD483E2BBF4C05E8AF10F5FA7626CFD3DC3092`.
- The Certum root and intermediate are shared public CAs, not an EVSign-account-specific trust root. Trusting either CA alone would authorize unrelated Certum customers.
- v0.4.7 embeds the exact RushRush Subject and compares the downloaded leaf certificate Subject for exact string equality. It does not contain the NaisNet Subject.
- The domestic COS URL and GitHub URL both serve the v0.4.10 EXE successfully. The observed failure is post-download publisher validation, not network or mirror availability.
- EVSign's public signing API accepts a license credential and optional certificate selector, then returns a signed file. Its public documentation exposes no client-verifiable account-level public key or account attestation.

## Non-goals

- Do not weaken verification to “any Authenticode signature with status Valid.”
- Do not trust the Certum root or intermediate as the Gift Panel publisher identity.
- Do not let the update server, COS pointer, GitHub branch, or Release metadata authorize a new publisher by itself.
- Do not overwrite, move, resign, or replace v0.4.7, v0.4.10, or their immutable assets.
- Do not assign one ordinary latest version to two byte-distinct executables.
- Do not promise automatic recovery for v0.4.7 when the domestic API is unreachable; its GitHub fallback cannot distinguish signer populations.
- Do not collect IP addresses, Bilibili identities, machine identifiers, or per-user update histories for migration telemetry.

## Domain language

**Artifact signer**
: The Authenticode leaf certificate that signs one EXE. It proves the downloaded binary carries a Windows-valid signature from one legal publisher.

**Publisher legal identity**
: The stable certificate fields `Country`, `Organization`, and `Subject.SerialNumber` or equivalent organization identifier. Leaf thumbprints and certificate serial numbers are not publisher identity.

**Rotation root**
: A non-exportable asymmetric Tencent Cloud KMS key that signs publisher-authorization policy. It is independent from EVSign and never signs application binaries.

**Publisher policy**
: Strict, canonical JSON signed by the rotation root. It authorizes legal publisher identities for exact roles, channels, tags, and time windows.

**Stable channel**
: The normal NaisNet update channel at `channels/stable/latest.json`.

**Legacy RushRush channel**
: The transitional channel at `channels/legacy-rushrush/latest.json`, allowed to serve only the exact RushRush bridge release.

**Enrollment client**
: v0.4.11 or later client that embeds the rotation-root public key, sends its canonical version to the domestic API, and verifies publisher policy.

**Bridge client**
: The exact v0.4.12 RushRush-signed transitional client that v0.4.7 accepts. It becomes an enrollment client immediately after installation and trusts future NaisNet artifacts only through signed publisher policy.

## Security invariants

1. Every update EXE must match trusted size and SHA-256 metadata before Authenticode policy is evaluated.
2. Windows Authenticode status must be `Valid`, with a Code Signing EKU and a system-valid chain.
3. A Windows-valid certificate is accepted only when its structured legal identity is authorized by a valid publisher policy for the exact tag, channel, and role.
4. A same-legal-identity certificate renewal may change thumbprint, leaf serial number, validity dates, and issuer chain details without requiring a new policy.
5. A different legal identity requires a higher publisher-policy epoch signed by the KMS rotation root.
6. The application server, update API, COS publisher, and ordinary Release workflow have no KMS signing permission.
7. Policy epochs and root epochs are monotonic. Published epoch objects are immutable and never replaced.
8. A client never accepts a policy epoch or root epoch lower than the highest it has persisted.
9. RushRush is authorized only for exact bridge tag `v0.4.12` on `legacy-rushrush`; it can never sign stable artifacts.
10. Existing Git tags and Releases are immutable. Recovery is forward-only through a higher version or higher policy epoch.

## Rotation root

Provision one Tencent Cloud KMS asymmetric signing key in `ap-shanghai`:

- key usage: asymmetric sign and verify only;
- algorithm: `ECC_P256_R1` with SHA-256;
- private key: generated and protected by KMS/HSM, non-exportable;
- public key: exported in SPKI DER form and committed after independent review;
- key ID exposed only as non-secret deployment configuration;
- key deletion protection and the maximum supported deletion waiting period enabled;
- CloudAudit enabled for create, enable, disable, sign, and deletion operations;
- CAM grants `SignByAsymmetricKey` only to the protected publisher-rotation identity;
- the Hosted server, update API service account, COS mirror identity, and normal Release workflow are explicitly denied signing access.

Tencent KMS supports `ECC_P256_R1`, signing and verification, downloading the public key for local verification, HSM-backed key protection, CAM controls, and CloudAudit. See:

- [Tencent KMS VerifyByAsymmetricKey](https://cloud.tencent.com/document/product/573/52064)
- [Tencent KMS asymmetric signing overview](https://cloud.tencent.com/document/product/573/53385)
- [Tencent KMS product overview](https://cloud.tencent.com/document/product/573/8780)
- [Tencent KMS audit logs](https://cloud.tencent.com/document/product/573/53523)

The repository stores only the SPKI public key and its SHA-256 key ID. No KMS credential or EVSign credential enters source, logs, artifacts, issues, or this design.

## Publisher policy format

The wire document is a strict envelope:

```json
{
  "signed": {
    "schemaVersion": 1,
    "epoch": 1,
    "issuedAt": "2026-09-01T00:00:00Z",
    "expiresAt": "2027-03-01T00:00:00Z",
    "minimumClientVersion": "0.4.11",
    "publishers": [
      {
        "id": "naisnet-cn-91210103ma7cj3c094",
        "country": "CN",
        "organization": "NaisNet Technology Co., Ltd.",
        "organizationId": "91210103MA7CJ3C094",
        "role": "stable",
        "allowedChannel": "stable"
      },
      {
        "id": "rushrush-bridge",
        "country": "CN",
        "organization": "RushRush Network Technology Ltd",
        "organizationId": "91450900MADM3GLG5P",
        "role": "bridge",
        "allowedChannel": "legacy-rushrush",
        "allowedTags": ["v0.4.12"],
        "validUntil": "2027-03-01T00:00:00Z"
      }
    ]
  },
  "signatures": [
    {
      "keyId": "sha256-of-kms-spki",
      "algorithm": "ECDSA_P256_SHA256",
      "value": "base64-der-ecdsa-signature"
    }
  ]
}
```

Rules:

- Reject unknown fields, duplicate JSON keys, trailing values, non-integer epochs, invalid timestamps, noncanonical tags, duplicate publisher IDs, and overlapping contradictory entries.
- Canonicalize only the `signed` object with a repository-owned deterministic JSON canonicalizer.
- Hash canonical bytes with SHA-256.
- Call KMS `SignByAsymmetricKey` with `MessageType=DIGEST` and `ECC_P256_R1`.
- Encode the returned ASN.1 DER ECDSA signature as Base64.
- Verify locally with the embedded SPKI public key and Go `ecdsa.VerifyASN1`.
- Bind authorization to publisher identity, artifact role, channel, tag, policy time window, and the update manifest's asset SHA-256.
- Keep RushRush authorization exact and temporary. A syntactically valid RushRush policy entry for any other tag or channel is invalid.

## Certificate identity verification

The Windows helper returns only:

- Authenticode status;
- leaf certificate DER encoded as Base64.

Go parses DER with `crypto/x509` and validates structured fields. It must not parse the human-formatted PowerShell Subject string.

The accepted stable NaisNet identity is:

- `Country = CN`;
- `Organization = NaisNet Technology Co., Ltd.`;
- `Subject.SerialNumber = 91210103MA7CJ3C094`;
- Code Signing EKU present;
- Authenticode status `Valid`.

Certificate thumbprint, certificate serial number, validity dates, and issuer chain may change during same-identity renewal. Any organization-identifier change requires a new KMS-signed policy epoch.

## Policy distribution and anti-rollback

Publish every epoch to immutable keys:

- COS: `trust/publisher/epochs/00000001.json`;
- GitHub: immutable asset attached to a dedicated `publisher-policy-epoch-00000001` Release.

Mutable discovery endpoints contain the complete signed policy, not an unsigned authorization:

- domestic: `/api/v1/trust/publisher-policy` backed by `trust/publisher/latest.json`;
- GitHub: a dedicated mutable publisher-trust ref serving `gift-panel-publisher-policy.json`.

The pointer may be stale or compromised without granting authority because the policy signature, epoch, and expiry are client-verified.

Clients:

1. start with embedded epoch 1;
2. fetch domestic and GitHub policy independently;
3. validate both without using data from one to validate the other;
4. choose the highest valid epoch;
5. atomically persist policy bytes, SHA-256, and highest epoch;
6. reject every lower epoch thereafter;
7. use an unexpired cached policy when both sources are unavailable;
8. after policy expiry, continue accepting only already-authorized current legal identities and never a new legal identity.

Root-key rotation uses separate strict root metadata. `rootEpoch+1` contains the new SPKI key and must be signed by the currently trusted root. A server response alone cannot replace the root.

## Version-aware channel selection

Enrollment clients send:

```text
X-Gift-Panel-Version: 0.4.11
```

The header contains only a canonical public application version.

Domestic API routing:

- missing header: legacy channel while legacy migration is active;
- canonical version lower than `0.4.11`: legacy channel;
- canonical version at or above `0.4.11`: stable channel;
- malformed, duplicate, oversized, prerelease, or otherwise noncanonical header: HTTP 400;
- response includes `Vary: X-Gift-Panel-Version` and remains `private, no-store`.

The API may log only aggregated version buckets and stable outcome codes. It must not add IPs, machine identifiers, Bilibili identities, cookies, tokens, or signed download URLs to application logs.

Storage pointers are independent:

- `channels/stable/latest.json`;
- `channels/legacy-rushrush/latest.json`.

The stable publisher cannot write the legacy pointer. The bridge publisher cannot write the stable pointer. Both immutable release prefixes and both pointer permissions are separately scoped in CAM.

## Three-stage migration

### Stage 1: v0.4.11 enrollment release

v0.4.11 is an ordinary NaisNet Release and GitHub latest.

It adds:

- embedded KMS SPKI public key;
- embedded epoch 1 policy;
- strict policy verification and cache;
- structured certificate identity matching;
- version request header;
- version-aware update status diagnostics.

Gates:

- real v0.4.10 to v0.4.11 update succeeds from domestic and GitHub;
- same NaisNet organization identity with a different leaf thumbprint passes;
- different organization ID fails despite Authenticode `Valid`;
- API receives and validates the version header;
- policy rollback, expiry, malformed JSON, bad KMS signature, and interrupted cache write tests pass;
- observe production for at least seven days;
- legacy routing stays inactive throughout Stage 1.

### Stage 2: v0.4.12 RushRush bridge

v0.4.12 uses a separate Bridge Release workflow:

- exact tag `v0.4.12`;
- GitHub Release `latest=false`;
- outer EXE signed by the reviewed RushRush certificate;
- embedded application update publisher determined by KMS policy, with NaisNet stable authorized;
- embedded FFmpeg remains the independently verified NaisNet-signed fixed component;
- no normal stable pointer mutation;
- immutable bridge asset mirrored only under the bridge release prefix;
- `legacy-rushrush/latest.json` advanced only after a separate confirmation.

Before activation:

- real public v0.4.7 automatically downloads and accepts the domestic RushRush bridge;
- real unversioned NaisNet client rejects the domestic RushRush bridge and successfully falls back to GitHub latest v0.4.11;
- v0.4.11 versioned client continues receiving stable v0.4.11 and never the legacy route;
- RushRush signed for another tag, stable channel, altered hash, or expired bridge authorization is rejected;
- observe for at least seven days after legacy pointer activation.

### Stage 3: v0.4.13 NaisNet convergence release

v0.4.13 is an ordinary NaisNet GitHub latest and stable release.

Acceptance requires:

- real v0.4.11 to v0.4.13 automatic update;
- real v0.4.12 bridge to v0.4.13 automatic update;
- both paths select the same highest policy epoch and NaisNet legal identity;
- domestic and GitHub hashes, signatures, manifests, and changelog match their reviewed release;
- observe for at least seven days before evaluating legacy-channel retirement.

The v0.4.12 bridge Release and immutable COS objects remain available during the support window. Retirement is a separate design and approval; it is not part of convergence publication.

## Workflow separation

### Ordinary Release workflow

- resolves the active normal signer profile;
- requires actual outer signer legal identity authorized for `stable` by the current policy;
- verifies embedded FFmpeg independently;
- publishes GitHub latest and stable-compatible assets;
- cannot call KMS or write the legacy pointer.

### Bridge Release workflow

- hard-codes exact allowed tag `v0.4.12`;
- resolves the reviewed RushRush artifact signer separately from the future NaisNet update trust;
- verifies the final outer signature is RushRush;
- verifies application-embedded policy root and baseline policy authorize future NaisNet stable updates;
- creates a complete immutable GitHub Release with `latest=false`;
- cannot write stable or mark itself latest;
- produces reviewed sidecar metadata for the legacy mirror.

### Publisher Rotation workflow

- uses protected environment `publisher-rotation`;
- requires a reviewed complete candidate policy and expected previous epoch;
- canonicalizes and validates before KMS signing;
- uses only short-lived, separately scoped KMS authorization;
- records non-secret KeyId, epoch, policy SHA-256, approver, KMS request ID, and CloudAudit event;
- publishes immutable epoch objects before advancing discovery pointers;
- never signs an EXE or modifies a Release.

## Failure handling and rollback

**Bad policy epoch**
: Never overwrite or delete it. Publish a higher corrective epoch. Clients that accepted the bad epoch cannot safely downgrade.

**v0.4.11 failure**
: Do not activate legacy. Keep stable on the last reviewed version for clients that have not upgraded, and publish a higher forward fix for upgraded clients.

**Bridge build or validation failure**
: Do not create or advance the legacy pointer. Stable, GitHub latest, and NaisNet clients remain unchanged.

**Bridge activation failure**
: Restore the reviewed previous legacy pointer or leave it absent. Never modify stable as part of bridge rollback.

**v0.4.13 failure**
: Restore the stable pointer only to protect clients that have not upgraded. Already upgraded clients require a higher forward-fix version; the updater never downgrades.

**Policy source unavailable**
: Use the other independently verified source or an unexpired cached policy. Never convert source failure into trust of server configuration.

**KMS unavailable**
: Ordinary same-identity NaisNet releases remain possible under the current unexpired policy. Cross-identity rotation and root rotation pause.

**KMS key compromise suspected**
: Disable signing permission, preserve CloudAudit, stop publisher rotation, and issue no new identity authorization. Root recovery follows a separately approved root-rotation or manual-client recovery procedure.

## Observability and privacy

Allowed aggregate dimensions:

- canonical client version bucket;
- selected channel;
- policy epoch;
- result codes such as `policy_valid`, `policy_rollback`, `publisher_not_authorized`, `bridge_fallback`, and `ready`;
- counts and bounded latency summaries.

Forbidden data:

- IP address in application diagnostics;
- Bilibili UID, nickname, avatar, room viewer identity, or gift content;
- machine ID, Windows username, file path containing a username;
- KMS credential, EVSign credential, signed COS URL, cookie, token, raw certificate private material;
- arbitrary exception text containing request or query data.

## Test and evidence matrix

### Unit and property tests

- strict policy JSON and deterministic canonical bytes;
- KMS ECDSA signature golden vectors;
- epoch and root-epoch monotonicity;
- timestamp and expiry boundaries with a pinned clock;
- structured X.509 legal identity extraction;
- same-identity renewal with different leaf certificate;
- different organization ID rejection;
- exact bridge tag/channel/role constraints;
- atomic policy-cache interruption recovery;
- fuzzing for duplicate fields, deep JSON, oversized documents, and ASN.1 certificate input.

### Integration tests

- update API missing, valid, duplicate, malformed, and boundary version headers;
- independent stable and legacy repositories and CAM-equivalent write seams;
- domestic policy stale while GitHub policy is newer, and the reverse;
- domestic RushRush verification failure followed by GitHub NaisNet success;
- bridge workflow cannot mark latest or modify stable;
- ordinary workflow cannot call KMS or modify legacy;
- publisher-rotation workflow cannot sign executables.

### Packaged Windows acceptance

- v0.4.10 to v0.4.11;
- public v0.4.7 to v0.4.12 bridge through domestic API;
- unversioned NaisNet client rejects bridge and falls back to GitHub v0.4.11;
- v0.4.11 and v0.4.12 both converge to v0.4.13;
- actual Authenticode chain, legal identity, SHA-256, policy epoch, root key ID, tag, GitHub latest flag, and COS channel pointer recorded;
- no signed URL query or credential appears in evidence.

## Approval boundaries

The following remain separate external mutations, each requiring action-time confirmation:

1. provision KMS key or CAM policy;
2. sign epoch 1 policy;
3. publish or advance a trust-policy pointer;
4. deploy version-aware API routing;
5. push/tag/publish v0.4.11;
6. push/tag/publish the v0.4.12 bridge;
7. advance `legacy-rushrush`;
8. push/tag/publish v0.4.13;
9. retire any legacy route, certificate, key, Release, COS object, or credential.

No implementation or rollout step inherits authorization from approval of this design.

## References

- [`docs/research/evsign-api-sign.md`](../../research/evsign-api-sign.md)
- [Tencent KMS VerifyByAsymmetricKey](https://cloud.tencent.com/document/product/573/52064)
- [Tencent KMS asymmetric signing overview](https://cloud.tencent.com/document/product/573/53385)
- [Tencent KMS product overview](https://cloud.tencent.com/document/product/573/8780)
- [Tencent KMS audit logs](https://cloud.tencent.com/document/product/573/53523)
