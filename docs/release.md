# Stable Release trust gates

`v0.4.12` is the first enrollment-enabled stable Release. This document
describes the Release contract only; it does not authorize changing the
application version, changelog, tag, GitHub Release, COS objects, KMS state, or
update pointers.

## New stable Releases

The ordinary stable workflow may build a new Release only for canonical
`v0.4.12` or later tags whose raw tag object and peeled commit match protected,
reviewed values. Before any build or signing step it requires all of the
following production inputs:

- canonical production P-256 root SPKI bytes, SHA-256, and public root key ID;
- a signed bootstrap policy, digest, and epoch that authorizes the exact
  NaisNet structured identity for the stable channel and tag;
- a separately signed authorization policy, digest, and strictly higher epoch that binds the
  final signed EXE SHA-256 to that same stable tag and NaisNet identity;
- the reviewed signed FFmpeg component-manifest SHA-256; and
- the closed stable EVSign certificate and structured NaisNet identity.

The bootstrap and final-hash policies are deliberately distinct. The bootstrap
policy is embedded in the executable; putting that executable's own final hash
inside the embedded bytes would create a circular artifact. Both policies are
verified under the same reviewed production root, and test-fixture trust
material is rejected.

After all target tests have run, the final Authenticode-valid EXE is sealed to a
content-addressed path. Reviewed tooling then verifies the sealed version, tag,
commit, embedded root and bootstrap bytes, both policy signatures and epochs,
the final-hash authorization, exact NaisNet identity, bundled and standalone
FFmpeg, checksums, and public evidence cross-bindings. No target code runs after
this seal. The workflow uploads a draft, downloads and hashes the exact draft
asset set, and only then publishes it with `latest=true`; it finally confirms
the Release through both the tag endpoint and `/releases/latest` and reads every
asset back again.

The stable workflow has no RushRush bridge credential, KMS signing operation,
legacy-pointer mutation, or COS credential. Policy signing and publication are
separate protected actions.

## Existing Release repair

An already published, complete pre-`v0.4.12` GitHub Release is detected from
verified GitHub Release state, not from an operator bypass flag. That historical
path remains data-only:
it downloads and verifies the existing assets and, where applicable, repairs
only the standalone FFmpeg validation closure. It does not enroll an old build,
rebuild, resign, replace conflicting assets, or claim that a historical release
contains the new trust material. An absent pre-`v0.4.12` Release is therefore a
hard failure, not permission to recreate history. Existing `v0.4.12` or later
enrollment Releases are immutable in this workflow and fail closed for a
separate audited recovery instead of being reinterpreted as historical repair.

## Action-time approvals

Preparing the protected inputs does not authorize release execution. Before
the first `v0.4.12` enrollment release, obtain fresh confirmation for the exact
version/changelog/tag changes and again for the proposed GitHub Release asset
closure. COS mirroring or stable-pointer advancement remains a separate action
and confirmation after the GitHub assets and packaged Windows acceptance have
been verified. No such action is performed by this implementation task.
