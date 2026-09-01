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
- the closed stable EVSign certificate and structured NaisNet identity.

Preparation runs target tests, signs once, and seals those exact Authenticode
bytes. It uploads a content-addressed Actions artifact containing the sealed
EXE, sealed FFmpeg closure, sidecars, candidate evidence, and digest-pinned
credential-free verification tools. It downloads that Actions artifact again
and requires every file to be byte-identical. Preparation cannot create or edit
a GitHub Release, attest Release assets, call KMS, or mutate COS/pointers.

Task 9 then signs a strictly higher authorization policy for that already
sealed candidate SHA-256. A separate manual **candidate publication** approval
must provide the exact protected preparation run ID, artifact ID/digest,
candidate SHA-256/size/tag/commit, raw tag object, tool hashes, root/bootstrap
inputs, and final authorization policy. Publication downloads that artifact;
it never rebuilds, reruns EVSign/RFC3161, executes target code, or receives
signer credentials. Any mismatch fails before a draft exists.

Publication revalidates the candidate and final-hash policy, then uses the
retained Go helper to create `gift-panel-windows-x64.exe` as a same-file hard
link to the content-addressed sealed EXE. The expected basename is uploaded
directly, without GitHub CLI label rewriting. The workflow reads back the exact
draft names/digests/bytes, rechecks the tag, publishes `latest=true`, and
confirms `/releases/latest`.

The public root key ID is never operator supplied. Evidence derives it
canonically as `sha256:<reviewed-spki-sha256>`. A separately audited provider
KMS identifier, if needed, must use a different cross-bound field.

The stable workflow has no RushRush bridge credential, KMS signing operation,
legacy-pointer mutation, or COS credential. Policy signing and publication are
separate protected actions.

## Existing Release repair

An already published, complete pre-`v0.4.12` GitHub Release is detected from
verified GitHub Release state, not from an operator bypass flag. That historical
path is a read-only verification job with only `contents: read`. It downloads
and verifies the existing EXE, sidecars, fallback manifest, changelog, and the
fixed sealed FFmpeg closure where applicable. It cannot upload, edit, attest,
rebuild, resign, or replace anything. An absent pre-`v0.4.12` Release is a hard
failure, not permission to recreate history. Existing `v0.4.12` or later
enrollment Releases remain immutable and require a separate audited recovery.

## Action-time approvals

The approvals are separate and ordered: (1) exact version/changelog/tag and
candidate preparation, (2) Task 9 policy rotation binding that immutable
candidate hash, and (3) manual publication of the reviewed candidate artifact.
COS mirroring or stable-pointer advancement remains another action after real
Windows acceptance. No such external action is performed by this task.
