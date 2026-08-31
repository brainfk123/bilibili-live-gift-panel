# Update trust root enrollment

The desktop enrollment build receives two public values through Go link flags:

- `APP_UPDATE_TRUST_ROOT_SPKI_B64`: a DER SubjectPublicKeyInfo for an ECDSA P-256 public key.
- `APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64`: the signed bootstrap publisher policy bytes.

Neither value is a credential. The build script validates canonical Base64 and
the P-256 SPKI locally, embeds the values only with `-ldflags -X`, and logs
only the SHA-256 of decoded input bytes.

## Current test-only fixture

The checked-in fixture is intentionally **not a production root**. It exists
only for local tests and enrollment seams:

- SPKI SHA-256: `5cd252fb0ce8932436faf8ccd1040981b89ee4ad6b9fe9e2a2b7e71aacb27cd3`
- Bootstrap policy SHA-256: `205b8ea9bf7e79d55292d63a1266a4882ab01fa5edb3eb79421724ddb9265d0e`

No production rotation root has been provisioned or independently reviewed.
Do not substitute a placeholder value or add a claimed production digest to
this document.

## Production enrollment review

After the production KMS key is provisioned, two reviewers independent of the
key operator must each obtain the public DER SPKI through the approved KMS
public-key export path and calculate its SHA-256 locally. They must compare
the two digests through the release change record, verify that the key is an
ECDSA P-256 public key, and separately verify the signed bootstrap policy's
canonical bytes, signature, epoch, expiry, and authorized publisher rules.

Only after both reviewers record the same SPKI and policy SHA-256 digests may
the release owner supply their Base64 encodings as
`APP_UPDATE_TRUST_ROOT_SPKI_B64` and
`APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64`, together with
`APP_UPDATE_TRUST_REQUIRED=1`. The release evidence must preserve the two
reviewer identities, KMS key reference, policy epoch, both public digests, and
the approval timestamp; it must never include private-key material or KMS
credentials.
