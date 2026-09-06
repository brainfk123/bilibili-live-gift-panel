# Version-Aware Signer Trust Rotation Design

## Status

Approved immediate-rollout design, amended 2026-09-02 to replace Tencent KMS/CAM/OIDC signing with a protected GitHub Environment Secret. Repository implementation and local verification do not themselves mutate GitHub or Tencent Cloud. Root-key rotation is not implemented by this design; the future sketch below requires a separate approved protocol design before any product or rollout claim.

## Goal

Allow supported Gift Panel clients to keep updating safely when the signing service renews a certificate for the same legal publisher, and to migrate automatically across publisher legal entities only after the independently embedded publisher-policy root authorizes that transition.

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
- Do not claim root-key rotation, root-epoch anti-rollback, or automatic root-compromise recovery in the immediate v0.4.7 → v0.4.11 → v0.4.12 rollout.

## Domain language

**Artifact signer**
: The Authenticode leaf certificate that signs one EXE. It proves the downloaded binary carries a Windows-valid signature from one legal publisher.

**Publisher legal identity**
: The stable certificate fields `Country`, `Organization`, and `Subject.SerialNumber` or equivalent organization identifier. Leaf thumbprints and certificate serial numbers are not publisher identity.

**Rotation root**
: An ECDSA P-256 key whose public SPKI is embedded by enrollment clients and whose PKCS#8 private key is available only to the protected `publisher-rotation` GitHub Environment signing job. It is independent from EVSign and never signs application binaries.

**Publisher policy**
: Strict, canonical JSON signed by the rotation root. It authorizes legal publisher identities for exact roles, channels, tags, and time windows.

**Stable channel**
: The normal NaisNet update channel at `channels/stable/latest.json`.

**Legacy RushRush channel**
: The transitional channel at `channels/legacy-rushrush/latest.json`, allowed to serve only the exact RushRush bridge release.

**Enrollment client**
: The v0.4.12 NaisNet client or v0.4.11 bridge client that embeds the rotation-root public key and verifies publisher policy. Both already send their canonical version through the existing User-Agent contract.

**Bridge client**
: The exact v0.4.11 RushRush-signed transitional client that v0.4.7 accepts. It becomes an enrollment client immediately after installation and trusts future NaisNet artifacts only through signed publisher policy.

## Security invariants

1. Every update EXE must match trusted size and SHA-256 metadata before Authenticode policy is evaluated.
2. Windows Authenticode status must be `Valid`, with a Code Signing EKU and a system-valid chain.
3. A Windows-valid certificate is accepted only when its structured legal identity is authorized by a valid publisher policy for the exact tag, channel, and role.
4. A same-legal-identity certificate renewal may change thumbprint, leaf serial number, validity dates, and issuer chain details without requiring a new policy.
5. A different legal identity requires a higher publisher-policy epoch signed by the rotation root.
6. The application server, update API, COS publication steps, and ordinary Release workflow never receive the rotation-root private key.
7. Publisher-policy epochs are monotonic. Published epoch objects are immutable and never replaced.
8. A client never accepts a publisher-policy epoch lower than the highest it has persisted.
9. RushRush is authorized only for exact bridge tag `v0.4.11` on `legacy-rushrush`; it can never sign stable artifacts.
10. Existing Git tags and Releases are immutable. Recovery is forward-only through a higher version or higher policy epoch.

## Rotation root

Generate one ECDSA P-256 key pair under the release operator's Windows account:

- private encoding: one unencrypted PKCS#8 PEM stored only as GitHub Environment Secret `PUBLISHER_ROTATION_PRIVATE_KEY_PEM` and a local Windows-DPAPI backup;
- public encoding: DER SubjectPublicKeyInfo committed at `publisher/rotation-root-spki.der` after its SHA-256 is reviewed;
- non-secret key label: GitHub variable `PUBLISHER_ROTATION_KEY_ID`, initially `publisher-root-v1`;
- public bindings: `PUBLISHER_ROTATION_SPKI_PATH=publisher/rotation-root-spki.der` and lowercase `PUBLISHER_ROTATION_SPKI_SHA256`;
- access boundary: only the `sign-policy` step references the private-key Secret; publication, Hosted, updater, mirror, bridge, and ordinary Release steps do not;
- audit correlation: `github-run:<run_id>:attempt:<run_attempt>` plus environment deployment history;
- account protection: the sole repository owner uses Passkey or hardware-backed MFA and does not grant repository write access to other users.

This deployment explicitly accepts an exportable GitHub Secret under the owner-approved threat model: GitHub account, GitHub-hosted runner, and pinned Actions are trusted, and repository write access remains owner-only. If that threat model changes, migrate the same `Signer` boundary to a non-exportable KMS before adding collaborators or untrusted runners.

The repository stores only the reviewed SPKI public key and non-secret metadata. The private key, EVSign credentials, and Tencent COS credentials never enter source, logs, artifacts, issues, or public audit documents.

## Publisher policy format

The wire document is a strict envelope:

```json
{
  "signed": {
    "epoch": 2,
    "expiresAt": "2027-03-01T00:00:00Z",
    "publishers": [
      {
        "id": "naisnet-primary",
        "role": "primary",
        "country": "CN",
        "organization": "NaisNet Technology Co., Ltd.",
        "organizationId": "91210103MA7CJ3C094",
        "allowedChannel": "stable",
        "allowedTags": ["v0.4.12"],
        "manifestSha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      },
      {
        "id": "rushrush-bridge",
        "role": "bridge",
        "country": "CN",
        "organization": "RushRush Network Technology Ltd",
        "organizationId": "91450900MADM3GLG5P",
        "allowedChannel": "legacy-rushrush",
        "allowedTags": ["v0.4.11"]
      }
    ]
  },
  "signatures": [
    {
      "algorithm": "ecdsa-p256-sha256",
      "signature": "base64-der-ecdsa-signature"
    }
  ]
}
```

Rules:

- Reject unknown fields, duplicate JSON keys, trailing values, non-integer epochs, invalid timestamps, noncanonical tags, duplicate publisher IDs, and overlapping contradictory entries.
- Canonicalize only the `signed` object with a repository-owned deterministic JSON canonicalizer.
- Hash canonical bytes with SHA-256.
- Parse exactly one PKCS#8 P-256 private key from the protected environment and verify that its public SPKI matches the reviewed SHA-256.
- Sign only the 32-byte SHA-256 digest and encode the ASN.1 DER ECDSA signature as Base64.
- Verify locally with the embedded SPKI public key and Go `ecdsa.VerifyASN1`.
- Bind authorization to publisher identity, artifact role, channel, tag, policy time window, and the update manifest's asset SHA-256.
- Keep RushRush authorization exact and temporary. A syntactically valid RushRush policy entry for any other tag or channel is invalid.

This schema is the authoritative client-compatible v1 wire contract for the immediate rollout. Fields such as `schemaVersion`, `issuedAt`, `minimumClientVersion`, signature `keyId`, or per-rule `validUntil` are not accepted by deployed v1 parsers; adding them requires a coordinated schema-v2 client, signer, publisher, cache, and bridge migration. The non-secret signing-key ID belongs in the separate audit document and readiness evidence, not in this signature object.

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

Certificate thumbprint, certificate serial number, validity dates, and issuer chain may change during same-identity renewal. Any organization-identifier change requires a new root-signed policy epoch.

## Policy distribution and anti-rollback

Publish every epoch to immutable keys:

- COS: `trust/publisher/epochs/00000001.json`;
- GitHub: immutable asset attached to a dedicated `publisher-policy-epoch-00000001` Release.

Every immutable policy Release has one exact three-asset contract: `gift-panel-publisher-policy.json`, `gift-panel-publisher-policy.audit.json`, and `gift-panel-publisher-policy.commit.json`, all `application/json`, with bounded sizes and GitHub digests. The commit asset contains the exact canonical bundle-marker bytes. Bridge tooling maps only those remote names into the retained local `policy.json`, `audit.json`, and `commit.json` triplet and rejects missing, duplicate, renamed, or extra Release assets.

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

**Future protocol sketch — not implemented or approved for this rollout:** a later root-key-rotation design may define strict root metadata in which `rootEpoch+1` contains a new SPKI and is signed by the currently trusted root. That sketch has no current client wire format, persistence contract, recovery semantics, workflow, test matrix, or Task 14 gate. A server response alone can never replace the root. Until a separate design is approved and implemented, suspected root compromise requires stopping policy rotation and using a separately approved manual recovery path.

## Version-aware channel selection

Every released updater from v0.4.7 onward already sends:

```text
User-Agent: bilibili-live-gift-panel/0.4.7
```

The domestic API uses this existing public version signal. No new enrollment header is required for the immediate migration. Future clients may add an explicit version header only as a separately designed defense-in-depth change; it is not part of this bridge.

Domestic API routing:

- exact reviewed legacy version `0.4.7`: legacy channel while legacy migration is active;
- exact bridge version `0.4.11`: stable channel;
- reviewed NaisNet versions `0.4.9`, `0.4.10`, `0.4.12`, and later canonical stable versions: stable channel;
- missing, duplicate, malformed, oversized, prerelease, development, or unrecognized User-Agent version: HTTP 400;
- response includes `Vary: User-Agent` and remains `private, no-store`.

Routing is an explicit allowlist, not a numeric less-than shortcut. Adding another legacy version requires independent evidence of its embedded signer and User-Agent behavior, a design amendment, and tests.

The API may log only aggregated version buckets and stable outcome codes. It must not add IPs, machine identifiers, Bilibili identities, cookies, tokens, or signed download URLs to application logs.

Storage pointers are independent:

- `channels/stable/latest.json`;
- `channels/legacy-rushrush/latest.json`.

The stable publisher cannot write the legacy pointer. The bridge publisher cannot write the stable pointer. Both immutable release prefixes and both pointer permissions are separately scoped in CAM.

## Two-release migration

### Stage 1: trust and routing preparation

Before publishing either migration artifact:

- generate the rotation root, protect its PKCS#8 PEM in the GitHub Environment, and review its public SPKI SHA-256;
- sign and publish epoch 1 policy;
- deploy strict User-Agent channel selection while leaving the legacy pointer absent;
- verify v0.4.7, v0.4.9, and v0.4.10 route decisions against captured real requests;
- verify missing, malformed, duplicate, and unrecognized versions fail closed;
- verify stable behavior is unchanged while legacy remains inactive.

### Stage 2: v0.4.12 NaisNet convergence release

v0.4.12 is an ordinary NaisNet Release, GitHub latest, and stable release.

It adds:

- embedded rotation-root SPKI public key;
- embedded epoch 1 policy;
- strict policy verification and cache;
- structured certificate identity matching;
- version-aware update status diagnostics.

The embedded epoch-1 document is bootstrap/enrollment policy only. Final release acceptance also requires a separately published higher authorization policy whose epoch advances the bootstrap and whose NaisNet stable rule binds the actual signed v0.4.12 EXE SHA-256 through the shared `AuthorizeAt` matcher. That exact-hash authorization policy remains external and is never required to be embedded in the already-built EXE.

Gates:

- real v0.4.9 and v0.4.10 to v0.4.12 updates succeed from domestic and GitHub;
- same NaisNet organization identity with a different leaf thumbprint passes;
- different organization ID fails despite Authenticode `Valid`;
- API parses the existing User-Agent versions and routes them to stable;
- policy rollback, expiry, malformed JSON, bad root signature, and interrupted cache write tests pass;
- observe production for at least seven days;
- legacy routing stays inactive throughout Stage 2.

### Stage 3: v0.4.11 RushRush bridge

v0.4.11 uses a separate Bridge Release workflow and is published only after the v0.4.12 stable observation gate passes:

- exact tag `v0.4.11`;
- GitHub Release `latest=false`;
- outer EXE signed by the reviewed RushRush certificate;
- embedded bootstrap policy enrolls the root/client, while a separate higher immutable authorization policy binds the actual NaisNet-signed v0.4.12 EXE hash;
- embedded FFmpeg remains the independently verified NaisNet-signed fixed component;
- no normal stable pointer mutation;
- immutable bridge asset mirrored only under the bridge release prefix;
- `legacy-rushrush/latest.json` advanced only after a separate confirmation.

Before activation:

- real public v0.4.7 automatically downloads and accepts the domestic RushRush bridge;
- real public v0.4.7 installs v0.4.11, restarts with User-Agent `0.4.11`, routes to stable, and then automatically installs NaisNet v0.4.12;
- v0.4.9 and v0.4.10 route directly to stable v0.4.12 and never receive the bridge;
- RushRush signed for another tag, stable channel, altered hash, or expired bridge authorization is rejected;
- domestic and GitHub hashes, signatures, manifests, and changelog match their reviewed releases;
- observe for at least seven days after legacy pointer activation before evaluating retirement.

The v0.4.11 bridge Release and immutable COS objects remain available during the support window. Retirement is a separate design and approval; it is not part of convergence publication.

## Workflow separation

### Ordinary Release workflow

- runs target checkout/build/test in an unprivileged job with no EVSign secret, selector, protected environment, or mutable publication capability and uploads a closed content-addressed unsigned handoff;
- uses a fresh protected signing runner that first downloads and byte-verifies that handoff, then checks out/builds reviewed signing and inspection tools, executes no target code, signs/seals/verifies, and uploads the exact signed candidate;
- publishes in a separate signer-free job;
- resolves the active normal signer profile only on the protected signing runner;
- requires actual outer signer legal identity authorized for `stable` by the current policy;
- verifies embedded FFmpeg independently;
- publishes GitHub latest and stable-compatible assets;
- cannot read the rotation-root Secret or write the legacy pointer.

### Bridge Release workflow

- hard-codes exact allowed tag `v0.4.11`;
- runs target checkout/build/test in an unprivileged job and hands off only a closed content-addressed unsigned artifact plus public readiness data;
- uses a fresh protected bridge-signing runner that downloads and verifies the handoff before obtaining reviewed tools, executes no target code, and signs/seals/verifies the exact bridge;
- uses a separate bridge publisher with no EVSign credential;
- resolves the reviewed RushRush artifact signer separately from the future NaisNet update trust;
- verifies the final outer signature is RushRush;
- verifies the embedded bootstrap policy independently from an external higher authorization policy that binds the actual v0.4.12 signed EXE hash and NaisNet signer;
- creates a complete immutable GitHub Release with `latest=false`;
- cannot write stable or mark itself latest;
- produces reviewed sidecar metadata for the legacy mirror.

### Publisher Rotation workflow

- uses protected environment `publisher-rotation`;
- requires a reviewed complete candidate policy and expected previous epoch;
- canonicalizes and validates before private-key use;
- injects the PKCS#8 Secret only into the protected signing step and clears its temporary byte copy after signer construction;
- records non-secret audit `keyId`, epoch, policy SHA-256, CI actor, GitHub run/attempt request ID, and UTC timestamp;
- publishes immutable epoch objects before advancing discovery pointers;
- never signs an EXE or modifies a Release.

## Failure handling and rollback

**Bad policy epoch**
: Never overwrite or delete it. Publish a higher corrective epoch. Clients that accepted the bad epoch cannot safely downgrade.

**v0.4.12 failure**
: Do not publish or activate the bridge. Keep stable on the last reviewed NaisNet version for clients that have not upgraded, and publish a higher forward fix for upgraded clients.

**Bridge build or validation failure**
: Do not create or advance the legacy pointer. Stable, GitHub latest, and NaisNet clients remain unchanged.

**Bridge activation failure**
: Restore the reviewed previous legacy pointer or leave it absent. Never modify stable as part of bridge rollback.

**Post-bridge NaisNet failure**
: Restore the stable pointer only to protect clients that have not upgraded. Already upgraded clients require a higher forward-fix version; the updater never downgrades.

**Policy source unavailable**
: Use the other independently verified source or an unexpired cached policy. Never convert source failure into trust of server configuration.

**Protected signing Secret unavailable**
: Ordinary same-identity NaisNet releases remain possible under the current unexpired policy. Cross-identity rotation pauses; restore the same reviewed Secret from its DPAPI backup only after comparing the derived SPKI digest.

**Rotation-root compromise suspected**
: Delete or replace the GitHub Environment Secret, disable the publisher-rotation workflow, pause update publication, preserve GitHub deployment/audit evidence, and issue no new identity authorization. Root recovery follows a separately approved root-rotation or manual-client recovery procedure.

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
- publisher root private key, EVSign credential, Tencent COS credential, signed COS URL, cookie, token, raw certificate private material;
- arbitrary exception text containing request or query data.

## Test and evidence matrix

### Unit and property tests

- strict policy JSON and deterministic canonical bytes;
- PKCS#8 P-256 parsing, reviewed-SPKI binding, digest-only ECDSA signature vectors, and malformed-key rejection;
- publisher-policy epoch monotonicity;
- timestamp and expiry boundaries with a pinned clock;
- structured X.509 legal identity extraction;
- same-identity renewal with different leaf certificate;
- different organization ID rejection;
- exact bridge tag/channel/role constraints;
- atomic policy-cache interruption recovery;
- fuzzing for duplicate fields, deep JSON, oversized documents, and ASN.1 certificate input.

### Integration tests

- update API exact v0.4.7, bridge v0.4.11, NaisNet versions, missing, duplicate, malformed, and unknown User-Agent values;
- independent stable and legacy repositories and CAM-equivalent write seams;
- domestic policy stale while GitHub policy is newer, and the reverse;
- domestic RushRush verification failure followed by GitHub NaisNet success;
- bridge workflow cannot mark latest or modify stable;
- ordinary workflow cannot read the rotation-root Secret or modify legacy;
- publisher-rotation workflow cannot sign executables.

### Packaged Windows acceptance

- v0.4.9 and v0.4.10 to NaisNet v0.4.12;
- public v0.4.7 to RushRush v0.4.11 bridge through domestic API;
- v0.4.11 bridge restarts, routes to stable, and converges to NaisNet v0.4.12;
- v0.4.9 and v0.4.10 never receive the legacy bridge;
- actual Authenticode chain, legal identity, SHA-256, policy epoch, root key ID, tag, GitHub latest flag, and COS channel pointer recorded;
- no signed URL query or credential appears in evidence.

## Approval boundaries

The following remain separate external mutations, each requiring action-time confirmation:

1. create the protected GitHub Environment Secret and reviewed public-root variables;
2. sign epoch 1 policy;
3. publish or advance a trust-policy pointer;
4. deploy version-aware API routing;
5. push/tag/publish NaisNet v0.4.12;
6. push/tag/publish the RushRush v0.4.11 bridge;
7. advance `legacy-rushrush`;
8. retire any legacy route, certificate, key, Release, COS object, or credential.

No implementation or rollout step inherits authorization from approval of this design.

## References

- [`docs/research/evsign-api-sign.md`](../../research/evsign-api-sign.md)
- [GitHub Actions secrets](https://docs.github.com/en/actions/concepts/security/secrets)
- [GitHub Actions secure use reference](https://docs.github.com/en/actions/reference/security/secure-use)
