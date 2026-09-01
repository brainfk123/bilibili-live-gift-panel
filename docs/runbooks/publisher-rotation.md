# Publisher-policy rotation runbook

This runbook is an approval boundary, not a rollout authorization. Repository code completion, a green CI run, or approval of the design does **not** authorize any Tencent Cloud or GitHub mutation.

## Four separate confirmations

Obtain and record a fresh confirmation immediately before each operation. One confirmation never carries into the next operation.

1. **Provision KMS and CAM.** Create or enable the `ap-shanghai` P-256 asymmetric signing key, deletion protection, CloudAudit coverage, GitHub OIDC provider, and the two separate CAM roles. Stop after public-only preflight.
2. **Sign one epoch.** Approve only the `sign-policy` deployment for the exact candidate epoch, exact previous epoch, reviewed KMS key ID, and reviewed SPKI SHA-256. This does not authorize upload.
3. **Publish immutable copies.** After reviewing the public signed bundle and audit, separately approve `publish-immutable`. This creates only `trust/publisher/epochs/%08d.json` and the dedicated `publisher-policy-epoch-%08d` GitHub Release/tag with the policy and public audit assets. This does not authorize discovery changes.
4. **Advance discovery pointers.** Dispatch with `advance_discovery=true`, verify both immutable readbacks again, then separately approve `advance-discovery`. This is the only approval that may change `trust/publisher/latest.json` and `refs/heads/publisher-trust` / `gift-panel-publisher-policy.json`.

The protected GitHub environment must be named exactly `publisher-rotation` and require reviewers for every protected job. Reject or leave a deployment pending when its separate confirmation is absent.

## Closed configuration and identities

There is intentionally no production SPKI or production SPKI digest in this repository yet. Before any protected run:

- independently export and review the P-256 SPKI DER;
- commit the reviewed **public** artifact at the fixed path `publisher/rotation-root-spki.der`;
- set `PUBLISHER_ROTATION_SPKI_PATH` to exactly `publisher/rotation-root-spki.der` and `PUBLISHER_ROTATION_SPKI_SHA256` to its lowercase SHA-256;
- set the non-secret KMS key ID, COS bucket/region, OIDC provider ID/audience, and exact CAM role ARNs in the protected environment;
- keep the candidate at the derived path `publisher/policy-candidates/epoch-%08d.json`; no workflow input may choose another path, key, tag, ref, or object key.

Until all of that reviewed public configuration exists, the workflow is expected to fail closed. Do not substitute `goserver/testdata/update-trust/root-epoch-1-spki.der`, another test key, or a made-up digest.

The KMS signing role may have only the exact equivalents of `kms:GetPublicKey` and `kms:SignByAsymmetricKey` for the reviewed key. It must have no COS or GitHub Release permission. The COS publication role may read/write only:

- `trust/publisher/epochs/%08d.json` for the exact candidate epoch, create-only;
- `trust/publisher/latest.json`, conditional write only during separately approved discovery advancement.

It must not list or delete objects and must not access release, stable, or legacy prefixes. GitHub `contents: write` exists only in immutable publication and discovery jobs. Ordinary Release, Hosted, update API, mirror, and bridge identities receive no KMS signing permission.

Every protected job exchanges GitHub OIDC for a 15-minute Tencent STS session. Signing sets `GIFT_PANEL_KMS_PROVIDER_MODE=environment-session` and supplies all three nonempty temporary values: `TENCENTCLOUD_SECRET_ID`, `TENCENTCLOUD_SECRET_KEY`, and `TENCENTCLOUD_SESSION_TOKEN`. Static credentials, tokenless credentials, TKE/CVM metadata, and ambient SDK fallback are prohibited.

## Public-only preflight

Run preflight from a reviewed operator shell. Redirect raw provider responses to a private temporary file and emit only the fields below. Do not print a public-key body, credential, token, policy document, signed URL, raw bundle path, or arbitrary provider error.

Example filters (verify current `tccli` response field names before use):

```bash
set -euo pipefail
umask 077

PRIVATE_TMP_ROOT=$(cd -- "${TMPDIR:-/tmp}" && pwd -P)
PRIVATE_TMP=$(mktemp -d -- "$PRIVATE_TMP_ROOT/publisher-preflight.XXXXXXXX")
case "$PRIVATE_TMP" in
  "$PRIVATE_TMP_ROOT"/publisher-preflight.*) ;;
  *) exit 1 ;;
esac
[ -d "$PRIVATE_TMP" ]
[ -O "$PRIVATE_TMP" ]
[ "$(stat -c '%a' -- "$PRIVATE_TMP")" = '700' ]
cleanup_private_tmp() {
  case "${PRIVATE_TMP:-}" in
    "$PRIVATE_TMP_ROOT"/publisher-preflight.*) rm -rf -- "$PRIVATE_TMP" ;;
    *) return 1 ;;
  esac
}
trap cleanup_private_tmp EXIT
trap 'exit 130' HUP INT TERM

tccli kms DescribeKey --region ap-shanghai --KeyId "$REVIEWED_KEY_ID" >"$PRIVATE_TMP/key.json"
jq '{region:"ap-shanghai",keyState:.KeyMetadata.KeyState,keyUsage:.KeyMetadata.KeyUsage,algorithm:.KeyMetadata.Type,deletionProtection:.KeyMetadata.DeletionProtection}' "$PRIVATE_TMP/key.json"

tccli kms GetPublicKey --region ap-shanghai --KeyId "$REVIEWED_KEY_ID" >"$PRIVATE_TMP/public.json"
jq -r '.PublicKey' "$PRIVATE_TMP/public.json" | base64 --decode >"$PRIVATE_TMP/root-spki.der"
printf '{"publicSpkiSha256":"%s"}\n' "$(sha256sum "$PRIVATE_TMP/root-spki.der" | cut -d' ' -f1)"

tccli cam GetPolicyVersion --PolicyId "$REVIEWED_POLICY_ID" --VersionId "$REVIEWED_POLICY_VERSION" >"$PRIVATE_TMP/cam.json"
jq '{camActions:[.PolicyVersion.Document.statement[].action[]] | unique | sort}' "$PRIVATE_TMP/cam.json"

tccli cloudaudit DescribeAuditTracks --region ap-shanghai >"$PRIVATE_TMP/audit.json"
jq '{cloudAuditAvailable:(.AuditTracks | type == "array" and length > 0)}' "$PRIVATE_TMP/audit.json"
```

The acceptable public-only result is exactly: region, key state, key usage, algorithm, deletion-protection status, public SPKI SHA-256, sorted CAM action names, and CloudAudit availability. Stop if the key is not enabled, not asymmetric sign/verify, not P-256, lacks deletion protection, has a different SPKI digest, exposes extra CAM actions/resources, or lacks CloudAudit.

Also use the CAM policy simulator to prove:

- the signing role allows only GetPublicKey/SignByAsymmetricKey on the one key and denies COS/GitHub release operations;
- the COS role allows only fixed trust-object Head/Get/conditional Put operations and denies Delete/List, stable, legacy, and ordinary release paths;
- ordinary Release and Hosted identities cannot call KMS signing.

Record only action names and allow/deny outcomes.

## Candidate and dry-run review

The candidate file is public but must be reviewed as code. Confirm exact candidate epoch, exact previous epoch, expiry, NaisNet stable rules, and (when present) the exact temporary RushRush bridge rule.

After the protected signing job produces the dedicated committed bundle, `trustpolicy verify-bundle`:

- retains and validates the policy, audit, and commit-marker files;
- checks the marker names, lengths, hashes, filesystem identity, ACLs, and close results;
- independently verifies the canonical client policy signature against the explicitly supplied reviewed P-256 SPKI and digest;
- checks the exact previous/current epoch transition and audit cross-binding;
- emits one captured canonical machine envelope.

`publish-trust-policy.mjs` consumes only that captured envelope; it never reopens policy or audit bundle paths. Its dry-run repeats the public-root signature, epoch, marker, audit, and hash checks and prints only fixed target names and hashes. Review that fixed summary before approving immutable publication.

## Immutable publication gate

For epoch `N`, the only immutable targets are:

- COS: `trust/publisher/epochs/%08d.json`;
- GitHub tag/Release: `publisher-policy-epoch-%08d`;
- GitHub assets: `gift-panel-publisher-policy.json` and `gift-panel-publisher-policy.audit.json`.

Uploads are create-only. Existing bytes are acceptable only when their exact SHA-256 matches. The publisher downloads the COS policy and both GitHub assets and compares exact bytes and hashes. Any upload error, conflicting existing object/Release, missing audit asset, or readback mismatch ends the job before either discovery pointer is read or written.

Every new or existing matching policy Release is patched to non-draft, non-prerelease with GitHub API `make_latest` set to the string value `"false"`. The publisher then reads the repository latest-Release endpoint and fails if the policy tag/Release is latest.

## Discovery advancement gate

Discovery advancement runs only when the typed workflow input is explicitly `true`. It first downloads and verifies both immutable copies again. It then requires both discovery sources to be absent when the expected previous epoch is zero, or to contain a correctly root-signed policy at exactly the expected previous epoch.

Both updates use compare-and-swap state captured from that read. No arbitrary COS key, GitHub tag, branch, ref, or path is accepted. Re-read both pointers and compare exact bytes after the conditional writes. A stale version/ETag, source mismatch, or concurrent change stops the operation.

Each source is classified independently as absent (allowed only when the expected previous epoch is zero), exact authenticated previous, exact candidate already applied, or invalid. An authenticated previous policy is a historical compare-and-swap anchor, so its canonical bytes, root signature, and exact epoch remain mandatory but its expiry does not block completion; the new candidate must still be unexpired. If one source already contains the exact candidate, update only the other. On a compare-and-swap conflict, re-read and reclassify once; accept only an exact candidate completed by the concurrent writer and never retry a blind overwrite.

## Audit and privacy

The public audit asset contains only the reviewed key ID, epoch, complete-policy SHA-256, provider request ID, UTC timestamp, and CI actor. GitHub environment deployment history supplies the approver and workflow run/attempt identity. Retain CloudAudit request correlation separately.

Never log policy contents, signatures, SPKI bytes, credentials, session tokens, signed URLs, raw bundle paths, environment values, or unfiltered provider responses. Preserve only fixed outcome codes, epochs, target names, hashes, actor/approver, workflow run/attempt, and provider request IDs.

## Failure, correction, and rollback

- **Signing failure:** publish nothing. Correct the candidate/configuration and obtain a new signing confirmation.
- **Immutable partial failure:** advance no pointer. Never overwrite or delete a successfully created immutable object or Release. Resolve the conflict and either complete the exact same epoch with identical bytes or publish a separately reviewed higher corrective epoch.
- **Pointer failure before a write:** leave both pointers unchanged and rerun only after re-reading the expected previous epoch.
- **One pointer advanced and the other failed:** do not downgrade or delete the signed higher policy. Freeze further rotations, preserve both readbacks and audit evidence, and complete the other pointer only after a fresh advancement confirmation and exact expected-state review. Clients safely authenticate either signed source and select the highest valid epoch.
- **Bad accepted policy:** rollback is never a lower epoch. Publish a higher corrective epoch signed by the trusted root. Clients that accepted a higher epoch must never be instructed to downgrade.
- **Suspected KMS compromise:** remove/disable SignByAsymmetricKey permission, preserve CloudAudit, stop all rotation jobs, and follow a separately approved root-recovery design. Do not use pointer edits as root recovery.

Immutable policies, tags, Releases, public audit assets, and CloudAudit evidence are retention records. Deletion, replacement, root rotation, or retirement requires a separate design and confirmation.
