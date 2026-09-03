# Streamlined Stable Release and Event-Driven Publisher Rotation Design

**Date:** 2026-09-03  
**Status:** Proposed for owner review; implementation requires a separate reviewed plan  
**Supersedes:** only the per-artifact policy approval requirement for stable versions after v0.4.12  
**Does not replace:** the embedded rotation root, monotonic policy epochs, exact RushRush bridge scope, or structured Authenticode identity checks

## Goal

After one compatibility transition in v0.4.13, an ordinary stable release signed by the already-authorized legal publisher must require one release trigger and no publisher-policy rotation. A previously unknown legal publisher must be handled when it is first observed: publication stops, a bounded review request is produced, and only an explicitly approved higher policy epoch can authorize it.

The release owner does not need to predict a future company name or organization identifier.

## Why the current flow is being changed

v0.4.12 was the first enrollment release and intentionally required two independent decisions:

1. EVSign produced an Authenticode signature for the candidate EXE.
2. The rotation root signed epoch 2, which bound the exact v0.4.12 tag and EXE SHA-256.

That was appropriate for the RushRush-to-NaisNet enrollment boundary. Repeating the same root signature, immutable policy Release, dual discovery advancement, and manual candidate-variable transfer for every same-publisher patch release creates operational cost without changing the accepted legal identity.

The first production run also established two workflow requirements:

- a draft Release must be addressed by its numeric Release ID because GitHub's tag lookup does not return the draft reliably;
- public candidate metadata must travel through job outputs and content-addressed artifacts, not through a manually maintained set of environment variables.

## Considered approaches

### A. Trust every certificate available through EVSign

Rejected. Authenticode does not contain the EVSign license or tenant identity, EVSign can select a certificate by service-side certificate ID, and shared EVSign certificates may be usable by other customers. The client cannot prove that a file came through this repository's EVSign account.

### B. Keep exact root approval for every stable EXE

Retains the strongest two-key release control but preserves the current operational burden. It remains available as an emergency or high-risk-release mode, but it is not the default ordinary release path.

### C. Authorize a bounded same-publisher tag window and rotate only on identity change

Selected. It is compatible with the deployed v1 policy schema and v0.4.12 parser, while making normal same-identity releases independent of the rotation-root private key. A finite tag window prevents an unbounded policy grant and can be extended by a reviewed epoch before it is exhausted.

## Trust model

### Identity, not EVSign service membership

Stable trust continues to compare the leaf certificate's structured legal identity:

- country;
- organization;
- subject organization identifier;
- Code Signing EKU;
- Authenticode status `Valid`.

Leaf thumbprint, certificate serial number, validity dates, and issuer-chain details may change without policy rotation when the structured legal identity remains equal. `X-Cert` remains fixed in CI to avoid accidental service-side certificate selection, but it is not a client trust anchor.

### Ordinary same-identity authorization

Policy epoch 3 will preserve the existing v1 wire schema and authorize:

- role `primary`;
- channel `stable`;
- the exact current NaisNet structured identity;
- a reviewed, sorted finite tag window from `v0.4.13` through `v0.4.32`;
- no `manifestSha256` field.

The existing exact RushRush bridge rule remains unchanged: only `v0.4.11` on `legacy-rushrush`.

The update manifest, Actions artifact digest, GitHub attestation, final Release asset digest, and Authenticode signature continue to bind each ordinary artifact's bytes. The rotation root no longer supplies a second per-artifact hash approval for tags in the stable window.

### Exact-hash mode remains supported

The policy and verifier retain exact `manifestSha256` authorization. It is required for:

- the immutable v0.4.12 enrollment evidence;
- a legal-publisher transition release unless a later reviewed design chooses a stricter mechanism;
- an explicitly selected high-risk release.

## Unexpected legal-publisher change

The protected signing job inspects the actual signed leaf certificate before any draft or public Release exists.

If the identity matches an active stable rule, the workflow continues automatically.

If the identity differs:

1. the release fails closed before publication;
2. CI uploads a public, bounded publisher-change request containing only the tag, artifact SHA-256, certificate DER SHA-256, structured identity, current policy epoch, and workflow run identity;
3. no root Secret is exposed and no trust pointer changes;
4. the owner reviews the newly observed legal identity and explicitly authorizes a publisher-rotation run;
5. the root-signed higher epoch authorizes the new primary identity and, for the transition release, its exact tag and artifact SHA-256;
6. old clients fetch the higher signed policy from COS or GitHub and can then accept the new publisher.

The publisher-policy candidate validator must accept a newly reviewed primary identity without that identity being compiled into an older client. It must still reject extra publishers, duplicate scopes, malformed identities, non-CN values under the current product policy, any RushRush scope expansion, and any unsigned or unreviewed request.

No valid public CA chain, EVSign certificate ID, display Subject string, or server response can authorize an unknown identity by itself.

## v0.4.13 compatibility transition

v0.4.13 is a forward hotfix for the oversized v0.4.12 package and the transition to ordinary same-identity release authorization.

The transition order is:

1. ship the existing UI-asset boundary fix that excludes `dist/ffmpeg-component` and `dist/release-ffmpeg-sealed`;
2. extend the domestic update API's exact stable User-Agent allowlist through `0.4.13` so the new client can continue checking for updates;
3. update the enrollment inspector so versions after v0.4.12 accept either an exact-hash stable rule or a hashless same-identity stable rule for the exact tag;
4. keep the v0.4.12 inspector path exact-hash-only;
5. build and Authenticode-sign the v0.4.13 candidate;
6. sign and publish epoch 3 with the finite NaisNet tag window and no manifest hash;
7. verify that the v0.4.12 updater accepts epoch 3 and the signed v0.4.13 artifact;
8. publish v0.4.13 as GitHub Latest and allow the existing stable mirror to advance COS;
9. observe v0.4.13 for seven days before adapting or activating the v0.4.11 bridge.

The published v0.4.12 tag, Release, assets, epoch-2 policy, and COS immutable objects remain unchanged.

## One-trigger ordinary release workflow

An ordinary release begins with one manual workflow dispatch containing the exact existing tag. That dispatch is the action-time publication confirmation.

The workflow keeps privilege separation:

1. `prepare-candidate` checks out the tag, builds/tests target code without credentials, builds the canonical changelog, downloads and verifies the fixed FFmpeg component, and uploads a closed unsigned handoff.
2. `sign-candidate` downloads and byte-verifies the handoff on a fresh protected runner, checks out digest-bound signing tools, signs through the fixed EVSign certificate selection, verifies the actual structured signer identity, and uploads a closed signed candidate.
3. `publish-candidate` receives only public job outputs and the signed artifact ID/digest, independently verifies the candidate, creates or resumes one exact draft, publishes it, verifies GitHub Latest, and leaves COS mirroring to the existing production mirror.

Public metadata passed through job outputs includes run ID, run attempt, artifact ID, artifact digest, candidate SHA-256, candidate size, tag, commit, tool hashes, and policy epoch/hash. No environment variable is manually copied between runs. Secrets never appear in outputs.

The ordinary workflow cannot read the publisher-rotation private key or write the legacy pointer.

## Draft Release transaction

Draft handling is numeric-ID based and idempotent:

1. list authenticated Releases and select exact `tag_name` matches, including drafts;
2. reject multiple matches or any conflicting published Release;
3. when absent, create a draft through the API and capture its numeric ID;
4. when one draft exists, accept only the reviewed tag, target commit, title, draft/prerelease state, and an empty, partial-valid, or complete-valid asset set;
5. upload only missing assets; never delete or replace an existing asset;
6. re-read the draft by numeric ID and compare every name, size, and digest;
7. patch that exact ID to `draft=false`, `prerelease=false`, and `make_latest=true`;
8. verify the published Release by numeric ID, tag lookup, and `/releases/latest`;
9. re-download and hash every required asset.

An extra, mismatched, duplicate, or replaced asset fails closed and requires explicit operator recovery. The workflow never deletes a draft or Release.

## Update routing

The API remains fail-closed and version aware. v0.4.13 is added as a reviewed stable client version. Missing, malformed, duplicate, development, prerelease, oversized, and unrecognized version signals continue to return HTTP 400.

This design does not introduce a blanket numeric `>=` parser. A later stable client version is added by the release change or covered by a separately tested bounded stable-version rule; legacy routing remains exact and independent.

## Failure handling

- Signing failure: publish nothing and retain no draft created by that run.
- Same-identity policy-window miss: stop before publication and request a policy-window extension.
- Unknown legal identity: produce the publisher-change request and stop before publication.
- Partial valid draft: resume by ID and upload only missing exact assets.
- Conflicting draft: stop; never delete or overwrite automatically.
- GitHub published but COS stale: keep GitHub immutable and retry the existing stable mirror; never rebuild or resign.
- Bad higher policy epoch: publish a higher corrective epoch; never roll back.
- v0.4.13 failure after adoption: recover only with a higher forward version.

## Verification

### Policy and client

- v0.4.12 parses and accepts the epoch-3 v1 document.
- v0.4.12 accepts NaisNet-signed v0.4.13 under the hashless stable rule.
- v0.4.12 rejects the same tag signed by a different organization identifier.
- v0.4.12 still requires the immutable exact-hash epoch-2 evidence when validating the v0.4.12 enrollment Release.
- exact-hash mode continues to reject a changed artifact hash.
- policy rollback and expiry behavior remain unchanged.

### Workflow

- one dispatch carries candidate metadata through job outputs without manually configured candidate variables;
- target code never reaches a signer or publisher credential;
- signing credentials never reach the publisher job;
- rotation-root credentials never reach the ordinary release workflow;
- zero, one valid, one partial-valid, one conflicting, and multiple draft cases are executed against a real local transaction harness;
- draft lookup never depends on the published-tag endpoint;
- numeric-ID publication and final Latest closure are verified.

### Packaged artifact

- a real Windows build is produced with both FFmpeg staging roots populated;
- `ui-assets.json` contains zero paths under those roots;
- embedded UI bytes remain bounded near the reviewed baseline;
- the final signed EXE size, PE sections, Authenticode status, structured identity, embedded FFmpeg closure, root SPKI, and bootstrap policy are recorded;
- real v0.4.9, v0.4.10, and v0.4.12 update checks select v0.4.13 from domestic and GitHub sources.

## Bridge impact

The existing v0.4.11 bridge workflow and its v0.4.12 convergence evidence must not be run unchanged after v0.4.13 becomes stable. A later bridge amendment must bind the then-current stable Release and policy, prove v0.4.7 → v0.4.11 → current stable on Windows, and begin its seven-day observation only after v0.4.13 has completed stable observation.

Bridge publication and legacy activation remain separate later approvals.

## Non-goals

- trusting all EVSign customers or all certificates under a public CA;
- predicting or pre-authorizing an unknown future company;
- automatic publisher-identity changes without owner approval;
- rotation-root replacement or compromise recovery;
- rewriting v0.4.12 history;
- activating or retiring the RushRush bridge in the v0.4.13 hotfix.

## Approval boundaries

Implementation approval does not authorize external mutations. The following remain action-time gates:

1. push the implementation and v0.4.13 release commit;
2. create the v0.4.13 tag;
3. sign and publish epoch 3 and advance policy discovery;
4. publish v0.4.13 as GitHub Latest;
5. manually intervene in a conflicting draft;
6. authorize any newly observed legal publisher;
7. activate, change, or retire legacy routing.
