# Stable Release trust gates

`v0.4.12` is the first enrollment-enabled stable Release. This document
describes the Release contract only; it does not authorize changing the
application version, changelog, tag, GitHub Release, COS objects, publisher-root Secret, or
update pointers.

## New stable Releases

The stable lifecycle is split across three fresh capabilities inside one manual
workflow run. Dispatch `.github/workflows/release.yml` with `operation=release`
and one existing canonical `v0.4.12` or later tag. The classifier resolves the
raw tag object and peeled commit from GitHub, rejects an existing published
Release, and passes those public bindings to the remaining jobs. Before build or
signing, preparation requires:

- canonical production P-256 root SPKI bytes and SHA-256;
- a signed bootstrap policy, digest, and epoch that authorizes the exact
  NaisNet structured identity for the stable channel and tag;
- the reviewed signed FFmpeg component-manifest SHA-256; and
- the reviewed tooling commit plus the protected SHA-256 of that checkout's
  `.github/changelog-history.json`; and
- no EVSign credential or selector; signer configuration is absent from this
  runner.

The unprivileged job runs target tests/build and uploads a closed
content-addressed unsigned handoff containing data only—never runner state or
candidate tools. A fresh protected `stable-sign` runner downloads and
byte-verifies that handoff before checking out/building reviewed signing and
inspection tools. It executes no target code, receives the closed stable
EVSign selector/credential only for signing, seals/verifies the exact result,
uploads a content-addressed signed candidate, reads it back, and requires every
file to be byte-identical. Separate `stable-publish` receives no signer
credential. No target-controlled `PATH`, `GITHUB_ENV`, tool, checkout, or
process survives from build into signing.

The protected signing job inspects the actual leaf certificate after EVSign
returns the signed file. The active NaisNet identity continues automatically.
An unknown legal identity creates a bounded `publisher-change-request-*`
artifact and stops before a publishable candidate is exposed; it requires a
separately reviewed higher publisher-policy epoch.

The publisher downloads the exact signed artifact from the same workflow run.
Artifact ID, artifact digest, candidate SHA-256/size, tool hashes, tag, and
commit are job outputs rather than manually copied `STABLE_CANDIDATE_*`
variables. Publication never rebuilds, reruns EVSign/RFC3161, executes target
code, or receives signer credentials.

The publication token has only `actions: read` plus the Release and attestation
writes it needs. The artifact metadata must bind to the current GitHub run ID,
exact artifact name, and digest, and must not be expired.

Publication downloads the exact three assets for the configured reusable
publisher-policy epoch, imports them into a private bundle, and verifies the
root signature and epoch transition against the committed rotation SPKI. It
then revalidates the candidate and the applicable exact-hash or
publisher-identity authorization, and uses the
retained Go helper to create `gift-panel-windows-x64.exe` as a same-file hard
link to the content-addressed sealed EXE. The expected basename is uploaded
directly, without GitHub CLI label rewriting. A bounded numeric-ID transaction
creates or resumes one exact draft, uploads only missing assets, never deletes
or replaces an asset, publishes `latest=true`, and verifies the numeric-ID,
tag, Latest, asset metadata, and downloaded bytes.

The public root key ID is never operator supplied. Evidence derives it
canonically as `sha256:<reviewed-spki-sha256>`. The publisher-policy audit uses
its separate reviewed key label and GitHub run/attempt request ID.

The stable workflow has no RushRush bridge credential, publisher-root private key,
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

For an ordinary release under the active primary identity and its finite tag
window, the `operation=release` dispatch is the publication confirmation. The
stable-sign and stable-publish environments retain their configured protection
rules, but no per-release candidate metadata or policy bytes are copied by the
operator. A newly observed legal identity still stops and requires explicit
review, a higher root-signed policy epoch, immutable publication, and discovery
advancement before the release can be retried. COS mirroring remains separate
from this workflow.
