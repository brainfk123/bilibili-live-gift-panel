# Publisher-policy rotation runbook

This runbook implements the owner-approved GitHub Environment Secret design. It does not use Tencent KMS, CAM federation, GitHub OIDC, Tencent STS, or CloudAudit. The publisher-policy wire format, client SPKI trust root, epoch rules, immutable targets, and dual COS/GitHub discovery flow are unchanged.

## Protected configuration

Create one GitHub Environment named exactly `publisher-rotation`. Only the repository owner may approve or modify it.

Required Environment Secret:

- `PUBLISHER_ROTATION_PRIVATE_KEY_PEM`: one unencrypted PKCS#8 PEM block of type `PRIVATE KEY` containing an ECDSA P-256 key.

Required variables:

- `PUBLISHER_ROTATION_KEY_ID=publisher-root-v1`;
- `PUBLISHER_ROTATION_SPKI_PATH=publisher/rotation-root-spki.der`;
- `PUBLISHER_ROTATION_SPKI_SHA256=<lowercase SHA-256 of that DER file>`;
- `PUBLISHER_COS_BUCKET=<existing release bucket>`;
- `PUBLISHER_COS_REGION=<existing release region>`.

Existing Tencent COS publication secrets:

- `TENCENT_CLOUD_SECRET_ID`;
- `TENCENT_CLOUD_SECRET_KEY`.

The signing job references only the root private-key Secret and public variables. The immutable-publication and discovery jobs reference only the COS credentials and GitHub token. No job receives both the root private key and COS/GitHub write credentials.

## Generate and back up the root

Generate the key under the release owner's Windows account. Keep no plaintext private-key file after the GitHub Secret is set.

Required outputs:

- public SPKI DER committed at `publisher/rotation-root-spki.der`;
- lowercase SHA-256 recorded as `PUBLISHER_ROTATION_SPKI_SHA256`;
- PKCS#8 PEM stored as `PUBLISHER_ROTATION_PRIVATE_KEY_PEM`;
- a DPAPI CurrentUser-encrypted backup stored outside Git, for example `E:\\bilibili\\.local-secrets\\publisher-rotation-root.pkcs8.dpapi`.

Before deleting any plaintext temporary copy, reconstruct the public SPKI from the private key and prove that its SHA-256 exactly matches the committed DER. Never print the PEM, DER bytes, Base64, or Secret value.

The checked-in `goserver/testdata/update-trust/root-epoch-1-spki.der` is test-only and must never be used as the production root.

## Public-only preflight

Preflight may display only:

- current branch and commit;
- Environment name;
- configured Secret and variable names, never values;
- root key ID;
- public SPKI path, length, and lowercase SHA-256;
- candidate epoch and expected previous epoch;
- fixed COS/GitHub target names.

Stop if the public SPKI is missing, not ECDSA P-256, differs from the reviewed SHA-256, or if the protected Secret/variables are absent.

## Workflow stages

The workflow has four closed stages:

1. `validate-candidate` validates the exact checked-in candidate with no credentials.
2. `sign-policy` runs in `publisher-rotation`, reads the PKCS#8 Secret, verifies its derived SPKI digest, signs only the SHA-256 digest, locally verifies the returned ASN.1 DER signature, and exports a public committed bundle.
3. `publish-immutable` receives no root private key. It creates or verifies the exact immutable COS object and dedicated non-latest GitHub policy Release.
4. `advance-discovery` runs only when the typed input `advance_discovery` is true and compare-and-swap advances the fixed COS and GitHub discovery pointers.

The audit `requestId` is `github-run:<run_id>:attempt:<run_attempt>`. The public audit contains only key ID, epoch, complete-policy SHA-256, request ID, UTC timestamp, and CI actor.

## Candidate and bundle review

The candidate path is derived only as `publisher/policy-candidates/epoch-%08d.json`. Confirm:

- exact candidate and previous epochs;
- unexpired RFC3339 expiry;
- NaisNet stable identity and exact allowed tags/hash;
- exact RushRush bridge rule only when required.

`trustpolicy verify-bundle` validates the policy, audit, commit marker, filesystem identity, canonical signature, reviewed SPKI, epoch transition, and audit cross-binding. The publisher consumes only that captured machine envelope and never reopens raw policy/audit paths.

## Unexpected stable publisher procedure

If EVSign returns a signed executable whose structured legal identity does not match the active primary rule, stop the stable release before creating or publishing a draft. Preserve the bounded `publisher-change-request-<tag>-<artifactSha256>` artifact produced by the release workflow; it contains only the exact tag, artifact and certificate DER hashes, structured identity, current policy epoch, and GitHub run identity.

Before authorizing that identity:

1. Compare every structured identity field and the certificate DER SHA-256 in the request with the actual EVSign account and certificate order. Neither a public CA chain, an EVSign certificate selector, nor a display Subject is authorization.
2. Create a new candidate file for exactly the next epoch. Enter the reviewed primary identity manually, bind the transition release to its exact tag and artifact SHA-256, and keep the primary tag list within the validator's finite bound.
3. Review the public candidate diff independently. Do not copy, transform, or automatically promote the request artifact into a candidate or signed policy.
4. Run the public-only candidate validation before protected signing.
5. Separately confirm the protected root-signing, immutable publication, and discovery-advancement run. A request artifact alone must never invoke or authorize any of these stages.

If any request field cannot be reconciled with the EVSign account, keep release and discovery paused. Existing signed policies and immutable objects are never edited; correction requires another explicitly reviewed higher epoch.

## Immutable targets

For epoch `N`:

- COS: `trust/publisher/epochs/%08d.json`;
- GitHub tag/Release: `publisher-policy-epoch-%08d`;
- assets: `gift-panel-publisher-policy.json`, `gift-panel-publisher-policy.audit.json`, and `gift-panel-publisher-policy.commit.json`.

Uploads are create-only. Existing objects are accepted only when exact bytes, sizes, SHA-256 values, media types, names, and release state match. A policy Release must remain non-draft, non-prerelease, and `make_latest=false`.

## Discovery advancement

Discovery advancement first re-verifies both immutable copies. Each pointer must be absent for epoch 1, the exact authenticated previous policy, or the exact candidate already applied. Invalid, stale, or conflicting content fails closed.

Updates use compare-and-swap and final paired rereads:

- COS pointer: `trust/publisher/latest.json`;
- GitHub ref/path: `refs/heads/publisher-trust` / `gift-panel-publisher-policy.json`.

Never downgrade a pointer or overwrite an immutable epoch.

## Failure and recovery

- Signing failure: publish nothing; correct the protected configuration or candidate.
- Immutable partial failure: advance no pointer; preserve successfully created immutable evidence.
- One pointer advanced: freeze further rotation and complete the other only after exact state review.
- Bad accepted policy: publish a higher corrective epoch; never roll back the epoch.
- Missing Secret: restore the same key from the DPAPI backup only after the derived SPKI digest matches.
- Suspected root leak: delete/replace the Environment Secret, disable publisher rotation, pause update publication, preserve GitHub audit/deployment evidence, and use the separately designed root-recovery path. Pointer edits cannot revoke a leaked embedded root.

Root-key rotation is not implemented by this rollout.
