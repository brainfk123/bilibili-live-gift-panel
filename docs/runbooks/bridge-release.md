# v0.4.11 RushRush bridge Release runbook

This runbook has two later, separately approved production actions. Repository
completion authorizes neither action.

1. Publish the exact `v0.4.11` GitHub bridge Release with
   `.github/workflows/bridge-release.yml`.
2. After real packaged acceptance, mirror the immutable bridge objects and
   advance `channels/legacy-rushrush/latest.json` through the separate legacy
   publication path.

Do not run either action until the NaisNet-signed `v0.4.12` stable Release has
completed at least seven full days of stable observation and that observation
has an independently reviewed record. Publishing the GitHub bridge does not
authorize mirroring, pointer advancement, legacy routing activation, or any
KMS change.

## Fixed roles and scope

- The bridge workflow is manual and accepts no tag input. It checks out only
  `refs/tags/v0.4.11` and fails for every other package version.
- The final outer EXE must have Windows Authenticode status `Valid` and the
  exact structured identity `CN` / `RushRush Network Technology Ltd` /
  `91450900MADM3GLG5P`.
- The application's embedded enrollment root and bootstrap policy authorize
  the NaisNet stable identity `CN` / `NaisNet Technology Co., Ltd.` /
  `91210103MA7CJ3C094`. The historical `APP_UPDATE_PUBLISHER` build value is
  therefore NaisNet; it is not evidence for the outer RushRush signature.
- Bundled and standalone FFmpeg are the same fixed, reviewed, NaisNet-signed
  component. The bridge workflow verifies the component Release, attestations,
  manifest digest, version, binary hash, size, Authenticode status, and
  structured NaisNet identity before and after packaging. It never signs,
  replaces, or republishes an FFmpeg component.
- The workflow can create only a draft GitHub Release, upload the closed asset
  set, read back and hash every byte, then publish it with `latest=false`. It
  cannot write stable or legacy pointers and has no COS or KMS credentials.

## Required protected configuration

Use the protected `bridge-release` environment. The bridge EVSign credential
and selector must be distinct from the stable profile.

Required environment secrets:

- `EVSIGN_BRIDGE_KEY`
- `EVSIGN_BRIDGE_PASSWORD`

Required reviewed environment variables:

- `EVSIGN_BRIDGE_CERTIFICATE`
- `EVSIGN_BRIDGE_PUBLISHER_IDENTITY`, exactly
  `{"country":"CN","organization":"RushRush Network Technology Ltd","organizationId":"91450900MADM3GLG5P"}`
- `BRIDGE_TRUST_ROOT_SPKI_B64`
- `BRIDGE_TRUST_ROOT_SPKI_SHA256`
- `BRIDGE_BOOTSTRAP_POLICY_B64`
- `BRIDGE_BOOTSTRAP_POLICY_SHA256`
- `BRIDGE_BOOTSTRAP_POLICY_EPOCH`
- `BRIDGE_FFMPEG_COMPONENT_MANIFEST_SHA256`
- `BRIDGE_REVIEWED_COMMIT_SHA`, the independently reviewed lowercase 40-hex
  commit peeled from immutable `refs/tags/v0.4.11`
- `BRIDGE_REVIEWED_TAG_OBJECT_SHA`, the exact raw tag-object SHA (equal to the
  commit only for a reviewed lightweight tag)
- `BRIDGE_TOOLING_COMMIT_SHA`, the protected reviewed commit used to prebuild
  the inspector, readiness verifier, bounded downloader, and EVSign frontend
- `BRIDGE_OBSERVATION_EVIDENCE_B64` and
  `BRIDGE_OBSERVATION_EVIDENCE_SHA256`
- `BRIDGE_PRODUCTION_TRUST_ATTESTATION_B64` and
  `BRIDGE_PRODUCTION_TRUST_ATTESTATION_SHA256`
- the existing reviewed `UPDATE_API_BASE_URL`

There are no production root, digest, policy, certificate selector, or
observation or trust-attestation values in the repository. The known Task 1
test root and policy digests are explicitly rejected. Until protected reviewers supply them,
the workflow intentionally fails before build. Never fill these fields with
test fixtures, placeholder digests, or locally generated production claims.

Protect `v0.4.11` with the repository tag ruleset: creation requires the
release-maintainer path, update and deletion are forbidden, and bypass is not
available to the bridge workflow. Record the ruleset ID and reviewed peeled
commit and raw tag-object SHA in the approval evidence. The workflow checks
local and remote raw and peeled refs after checkout, immediately before draft
creation, and immediately before `draft=false`. An annotated tag must have one
raw object equal to `BRIDGE_REVIEWED_TAG_OBJECT_SHA` and one peeled commit equal
to `BRIDGE_REVIEWED_COMMIT_SHA`; a lightweight tag must have one raw ref equal
to both values and no peeled line. A peeled-only ref, rewrite to a new tag
object on the same commit, ambiguity, or any movement stops publication.

## Gate A: later GitHub bridge publication

Before requesting approval, record read-only evidence for:

- `refs/tags/v0.4.11` commit and `package.json` version `0.4.11`;
- absence of an existing GitHub Release for `v0.4.11`;
- the actual immutable GitHub `v0.4.12` Release ID, tag, and `published_at`;
- a reviewed observation-evidence artifact whose digest binds that Release ID,
  tag, `published_at`, exact v0.4.12 executable SHA-256, seven-day end, passing
  result, and review time; the hash must also match Release asset metadata and
  the bounded-download checksum sidecar;
- daily bounded updater and policy result counts for the full observation;
- the immutable `publisher-policy-epoch-00000001` Release and exact
  `policy.json`/`audit.json`/`commit.json` bytes, with API metadata
  size/digest/content-type validation before bounded streaming download;
- a reviewed production-trust attestation binding root SPKI SHA-256, exact
  epoch-1 policy bytes/hash/epoch, KMS key ID/request ID/audit digest, immutable
  policy Release ID/tag/time/assets, and review time;
- two-reviewer agreement on all of those digests and the fixed FFmpeg
  component-manifest SHA-256;
- exact protected environment variable names and bridge-only secret names,
  without printing their values.

Stop on any mismatch, existing Release, incomplete observation, missing review,
or unavailable fixed FFmpeg component. Show the intended workflow run, tag,
commit, immutable asset names, and rollback boundary, then obtain explicit
approval for GitHub publication only.

After approval, dispatch the dedicated workflow once. Do not use the ordinary
Release workflow and do not repair an existing `v0.4.11` Release. A failed run
may leave a draft; inspect it without mutation and require a new recovery
decision. Never convert an unverified draft to non-draft manually.

Acceptance evidence must include:

- GitHub API `draft=false`, `prerelease=false`, and the bridge is not the
  `/releases/latest` result;
- exact asset names, sizes, API digests, downloaded bytes, and
  `SHA256SUMS.txt`;
- the Task 7 mirror closure assets `gift-panel-windows-x64.exe`, its strict
  `.sha256`, `gift-panel-update.json`, and schema-valid
  `gift-panel-changelog.json`, revalidated with the updateapi ByTag/mirror
  implementation before the draft is created;
- RushRush outer structured identity and Authenticode status;
- NaisNet standalone FFmpeg structured identity, version, hash, size, and
  component-manifest hash;
- embedded root digest, bootstrap policy epoch/hash, and client-side policy
  verification authorizing NaisNet stable `v0.4.12`;
- matching Authenticode PE-content digest between preserved unsigned bytes and
  the signed output, plus final extraction/reverification of embedded trust and
  FFmpeg from the bound signed artifact;
- Task9 `trustpolicy verify-bundle` machine output binding the committed policy
  and audit, including nonempty KMS request ID and CI actor;
- the shared production client policy verifier authorizing the exact input
  `{tag:v0.4.12, channel:stable, sha256:<actual Release EXE>, NaisNet identity}`;
  the epoch-1 stable rule must carry that exact `manifestSha256`.
- proof that stable and legacy pointers did not change.

GitHub acceptance completes Gate A only. Keep legacy routing inactive.

## Gate B: later mirror and legacy-pointer activation

Gate B is a different production action and needs a new explicit approval.
Begin only after Gate A acceptance and controlled real-Windows package tests
prove all of the following:

1. a public `v0.4.7` installation downloads and accepts RushRush `v0.4.11`;
2. `v0.4.11` restarts with canonical User-Agent version `0.4.11`;
3. `v0.4.11` routes to stable and accepts NaisNet `v0.4.12` through the
   embedded signed policy;
4. public `v0.4.9` and `v0.4.10` clients remain on stable and never receive
   `v0.4.11`;
5. stable object bytes and `channels/stable/latest.json` are unchanged.

Show the exact immutable mirror object keys, proposed
`channels/legacy-rushrush/latest.json` bytes, current pointer or absence, legacy
routing change, and rollback target. Obtain separate confirmation before the
mirror upload, then separate confirmation before pointer/routing activation if
the deployment procedure splits those effects. This bridge workflow must never
be rerun as a pointer-activation mechanism.

If acceptance fails, leave the stable channel unchanged, do not advance the
legacy pointer, and keep legacy routing inactive. Forward repair requires a new
reviewed design; never mutate the published bridge assets, tag, Release, signed
policy epoch, or stable pointer in place.
