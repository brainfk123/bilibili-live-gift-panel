# Stable Release trust gates

`v0.4.12` is the first enrollment-enabled stable Release. This document
describes the Release contract only; it does not authorize changing the
application version, changelog, tag, GitHub Release, COS objects, KMS state, or
update pointers.

## New stable Releases

The stable lifecycle is deliberately two-stage. A push of a new canonical
`v0.4.12` or later tag may enter **candidate preparation only**. Its raw tag
object and peeled commit must match protected reviewed values. Before build or
signing, preparation requires:

- canonical production P-256 root SPKI bytes and SHA-256;
- a signed bootstrap policy, digest, and epoch that authorizes the exact
  NaisNet structured identity for the stable channel and tag;
- the reviewed signed FFmpeg component-manifest SHA-256; and
- the reviewed tooling commit plus the protected SHA-256 of that checkout's
  `.github/changelog-history.json`; and
- the closed stable EVSign certificate and structured NaisNet identity.

Preparation runs target tests, signs once, and seals those exact Authenticode
bytes. It uploads a content-addressed Actions artifact containing the sealed
EXE, sealed FFmpeg closure, sidecars, candidate evidence, and digest-pinned
credential-free verification tools. It downloads that Actions artifact again
and requires every file to be byte-identical. Preparation cannot create or edit
a GitHub Release, attest Release assets, call KMS, or mutate COS/pointers.
Reviewed tooling and target source use independent `tooling/` and `source/`
checkout roots, so target cleanup cannot erase the tool root. Target
npm/Go/tests/build steps receive no EVSign selector, key, password, or
certificate variable; only the narrow reviewed signing step receives them.

Task 9 then signs a strictly higher authorization policy for that already
sealed candidate SHA-256. A separate manual **candidate publication** approval
must provide the exact protected preparation run ID, artifact ID/digest,
candidate SHA-256/size/tag/commit, raw tag object, tool hashes, root/bootstrap
inputs, reviewed changelog-history/tooling provenance, and final authorization
policy. Publication downloads that artifact;
it never rebuilds, reruns EVSign/RFC3161, executes target code, or receives
signer credentials. Any mismatch fails before a draft exists.

The publication token has only `actions: read` plus the Release and attestation
writes it needs. Before cross-run download, the exact successful push run must
match repository/head repository, reviewed workflow ID/path, tag, commit, run
attempt, completed/success state, and non-fork provenance. Artifact
ID/name/digest/creation/expiry must bind to that run and the reviewed inputs.

Publication revalidates the candidate and final-hash policy, then uses the
retained Go helper to create `gift-panel-windows-x64.exe` as a same-file hard
link to the content-addressed sealed EXE. The expected basename is uploaded
directly, without GitHub CLI label rewriting. The workflow reads back the exact
draft names/digests/bytes, rechecks the tag, publishes `latest=true`, and
confirms `/releases/latest`.
After publication it queries the Release again, requires the exact asset
names/digests/sizes, downloads and byte-verifies every asset again, and only
then accepts the matching `/releases/latest` result.

The public root key ID is never operator supplied. Evidence derives it
canonically as `sha256:<reviewed-spki-sha256>`. A separately audited provider
KMS identifier, if needed, must use a different cross-bound field.

The stable workflow has no RushRush bridge credential, KMS signing operation,
legacy-pointer mutation, or COS credential. Policy signing and publication are
separate protected actions.

## Existing Release verification

An already published, complete pre-`v0.4.12` GitHub Release is detected from
verified GitHub Release state, not from an operator bypass flag. That historical
path is a read-only verification job with only `contents: read`. It downloads
and verifies the existing EXE, sidecars, fallback manifest, changelog, and the
fixed sealed FFmpeg closure where applicable. It cannot upload, edit, attest,
rebuild, resign, or replace anything. An absent pre-`v0.4.12` Release is a hard
failure, not permission to recreate history. Existing `v0.4.12` or later
enrollment Releases remain immutable and require a separate audited recovery.
Supported historical identities are closed: `v0.4.7` is exact structured
RushRush; `v0.4.9` and `v0.4.10` are exact structured NaisNet. No other
pre-enrollment tag inherits a signer rule.

Candidate extraction rejects reparse points, symlinks/junctions, unexpected
or empty directories, and files outside the exact closed set before downloaded
tools execute. The target `gift-panel-changelog.json` must contain exactly one
strict release entry matching the current tag/version. History comes only from
the reviewed tooling checkout's `.github/changelog-history.json`; its exact
bytes must match the protected lowercase SHA-256 and the checkout must match the
reviewed tooling commit. There is no tag enumeration, hosted Release download,
or fallback source. History is strict, bounded to 256 KiB, descending,
duplicate-free, and entirely older than the current release. Candidate and
published evidence expose only the canonical changelog hash, reviewed history
hash, and tooling commit—never paths. The deterministic merge fails on missing,
malformed, mismatched, duplicate, current-colliding, or future history.
For the first enrollment Release, `v0.4.12`, that reviewed history must begin
with the exact sequence `0.4.10`, `0.4.9`, `0.4.7`; `0.4.8` is not synthesized.
Later stable versions continue to require reviewed history bytes and their
protected digest and may add their own separately reviewed sequence invariant.
The tooling checkout's history file and protected
`STABLE_CHANGELOG_HISTORY_SHA256` value are one review unit and must be reviewed
and updated together before candidate preparation or publication.

## Action-time approvals

The approvals are separate and ordered: (1) exact version/changelog/tag and
candidate preparation, (2) Task 9 policy rotation binding that immutable
candidate hash, and (3) manual publication of the reviewed candidate artifact.
COS mirroring or stable-pointer advancement remains another action after real
Windows acceptance. No such external action is performed by this task.
