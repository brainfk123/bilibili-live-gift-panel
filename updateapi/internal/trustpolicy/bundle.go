package trustpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

const (
	BundleCommitFileName      = "commit.json"
	BundleCommitSchemaVersion = 1
	maxBundleCommitBytes      = 4 << 10
	maxBundleAuditBytes       = 64 << 10
)

// BundleArtifact binds one committed filename to its exact bytes.
type BundleArtifact struct {
	Name   string `json:"name"`
	Length uint64 `json:"length"`
	SHA256 string `json:"sha256"`
}

// BundleCommit is the canonical marker written after both bundle artifacts.
type BundleCommit struct {
	SchemaVersion uint64         `json:"schemaVersion"`
	Policy        BundleArtifact `json:"policy"`
	Audit         BundleArtifact `json:"audit"`
}

// CommittedBundle contains bytes that passed marker and content validation.
type CommittedBundle struct {
	Policy []byte
	Audit  []byte
	Commit BundleCommit
}

type committedPolicyDocument struct {
	Signed     json.RawMessage   `json:"signed"`
	Signatures []policySignature `json:"signatures"`
}

const errCommittedBundle safeError = "publisher policy bundle is invalid"

// BuildBundleCommit constructs the canonical, non-secret commit marker.
func BuildBundleCommit(policyName string, policy []byte, auditName string, audit []byte) ([]byte, error) {
	if !validBundleArtifactName(policyName) || !validBundleArtifactName(auditName) || policyName == auditName ||
		policyName == BundleCommitFileName || auditName == BundleCommitFileName || len(policy) == 0 || len(policy) > maxPolicyBytes ||
		len(audit) == 0 || len(audit) > maxBundleAuditBytes {
		return nil, errCommittedBundle
	}
	policyDigest := sha256.Sum256(policy)
	auditDigest := sha256.Sum256(audit)
	marker, err := json.Marshal(BundleCommit{
		SchemaVersion: BundleCommitSchemaVersion,
		Policy:        BundleArtifact{Name: policyName, Length: uint64(len(policy)), SHA256: hex.EncodeToString(policyDigest[:])},
		Audit:         BundleArtifact{Name: auditName, Length: uint64(len(audit)), SHA256: hex.EncodeToString(auditDigest[:])},
	})
	if err != nil || len(marker) == 0 || len(marker) > maxBundleCommitBytes {
		return nil, errCommittedBundle
	}
	return marker, nil
}

// ValidateCommittedBundle validates canonical marker, exact bytes, and the
// client-compatible policy/audit schemas before returning trusted copies.
func ValidateCommittedBundle(policyName string, policy []byte, auditName string, audit []byte, marker []byte) (CommittedBundle, error) {
	if !validBundleArtifactName(policyName) || !validBundleArtifactName(auditName) || policyName == auditName ||
		len(policy) == 0 || len(policy) > maxPolicyBytes || len(audit) == 0 || len(audit) > maxBundleAuditBytes ||
		len(marker) == 0 || len(marker) > maxBundleCommitBytes {
		return CommittedBundle{}, errCommittedBundle
	}
	if err := validateCandidateJSON(marker); err != nil {
		return CommittedBundle{}, errCommittedBundle
	}
	decoder := json.NewDecoder(bytes.NewReader(marker))
	decoder.DisallowUnknownFields()
	var commit BundleCommit
	if err := decoder.Decode(&commit); err != nil {
		return CommittedBundle{}, errCommittedBundle
	}
	canonicalMarker, err := json.Marshal(commit)
	if err != nil || !bytes.Equal(canonicalMarker, marker) || commit.SchemaVersion != BundleCommitSchemaVersion ||
		commit.Policy.Name != policyName || commit.Audit.Name != auditName || commit.Policy.Name == commit.Audit.Name ||
		commit.Policy.Length != uint64(len(policy)) || commit.Audit.Length != uint64(len(audit)) ||
		!sha256Hex.MatchString(commit.Policy.SHA256) || !sha256Hex.MatchString(commit.Audit.SHA256) {
		return CommittedBundle{}, errCommittedBundle
	}
	policyDigest := sha256.Sum256(policy)
	auditDigest := sha256.Sum256(audit)
	if commit.Policy.SHA256 != hex.EncodeToString(policyDigest[:]) || commit.Audit.SHA256 != hex.EncodeToString(auditDigest[:]) {
		return CommittedBundle{}, errCommittedBundle
	}
	epoch, err := validateCommittedPolicy(policy)
	if err != nil || validateCommittedAudit(audit, hex.EncodeToString(policyDigest[:]), epoch) != nil {
		return CommittedBundle{}, errCommittedBundle
	}
	return CommittedBundle{
		Policy: append([]byte(nil), policy...),
		Audit:  append([]byte(nil), audit...),
		Commit: commit,
	}, nil
}

func validateCommittedPolicy(policy []byte) (uint64, error) {
	if err := validateCandidateJSON(policy); err != nil {
		return 0, errCommittedBundle
	}
	decoder := json.NewDecoder(bytes.NewReader(policy))
	decoder.DisallowUnknownFields()
	var document committedPolicyDocument
	if err := decoder.Decode(&document); err != nil || len(document.Signed) == 0 || len(document.Signatures) != 1 ||
		document.Signatures[0].Algorithm != clientAlgorithm {
		return 0, errCommittedBundle
	}
	var probe Candidate
	candidateDecoder := json.NewDecoder(bytes.NewReader(document.Signed))
	candidateDecoder.DisallowUnknownFields()
	if err := candidateDecoder.Decode(&probe); err != nil || probe.Epoch == 0 {
		return 0, errCommittedBundle
	}
	candidate, err := ParseCandidate(document.Signed, CandidateOptions{ExpectedPreviousEpoch: probe.Epoch - 1, Now: time.Now().UTC()})
	if err != nil {
		return 0, errCommittedBundle
	}
	canonicalSigned, err := CanonicalSigned(candidate)
	if err != nil || !bytes.Equal(canonicalSigned, document.Signed) {
		return 0, errCommittedBundle
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(document.Signatures[0].Signature)
	if err != nil || !validP256DERSignature(signature) {
		return 0, errCommittedBundle
	}
	signatures, err := json.Marshal(document.Signatures)
	if err != nil {
		return 0, errCommittedBundle
	}
	want := make([]byte, 0, len(canonicalSigned)+len(signatures)+28)
	want = append(want, `{"signed":`...)
	want = append(want, canonicalSigned...)
	want = append(want, `,"signatures":`...)
	want = append(want, signatures...)
	want = append(want, '}')
	if !bytes.Equal(want, policy) {
		return 0, errCommittedBundle
	}
	return candidate.Epoch, nil
}

func validateCommittedAudit(data []byte, policySHA256 string, epoch uint64) error {
	if err := validateCandidateJSON(data); err != nil {
		return errCommittedBundle
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var audit Audit
	if err := decoder.Decode(&audit); err != nil {
		return errCommittedBundle
	}
	canonical, err := json.Marshal(audit)
	if err != nil || !bytes.Equal(canonical, data) || !keyIDValue.MatchString(audit.KeyID) || audit.Epoch != epoch ||
		audit.PolicySHA256 != policySHA256 || !requestID.MatchString(audit.RequestID) || !ciActor.MatchString(audit.CIActor) {
		return errCommittedBundle
	}
	timestamp, err := time.Parse(time.RFC3339, audit.UTC)
	if err != nil || !strings.HasSuffix(audit.UTC, "Z") || timestamp.Format(time.RFC3339) != audit.UTC {
		return errCommittedBundle
	}
	return nil
}

func validBundleArtifactName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}
