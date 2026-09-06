package trustpolicy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestCommittedBundleMarkerIsCanonicalAndValidatesPolicyAuditPair(t *testing.T) {
	policy, audit := validCommittedBundlePair(t)
	marker, err := BuildBundleCommit("policy.json", policy, "audit.json", audit)
	if err != nil {
		t.Fatal(err)
	}
	policyDigest := sha256.Sum256(policy)
	auditDigest := sha256.Sum256(audit)
	want := `{"schemaVersion":1,"policy":{"name":"policy.json","length":` + decimalLength(len(policy)) + `,"sha256":"` + hex.EncodeToString(policyDigest[:]) + `"},"audit":{"name":"audit.json","length":` + decimalLength(len(audit)) + `,"sha256":"` + hex.EncodeToString(auditDigest[:]) + `"}}`
	if string(marker) != want {
		t.Fatalf("marker = %s\nwant = %s", marker, want)
	}
	committed, err := ValidateCommittedBundle("policy.json", policy, "audit.json", audit, marker)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed.Policy, policy) || !bytes.Equal(committed.Audit, audit) || committed.Commit.SchemaVersion != 1 {
		t.Fatal("validated bundle differs from committed pair")
	}
}

func TestCommittedBundleValidationRejectsAmbiguousMarkerAndMalformedFiles(t *testing.T) {
	policy, audit := validCommittedBundlePair(t)
	marker, err := BuildBundleCommit("policy.json", policy, "audit.json", audit)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		policy     []byte
		audit      []byte
		marker     []byte
		policyName string
		auditName  string
	}{
		{name: "unknown marker field", policy: policy, audit: audit, marker: bytes.Replace(marker, []byte(`{"schemaVersion":1`), []byte(`{"schemaVersion":1,"unknown":true`), 1), policyName: "policy.json", auditName: "audit.json"},
		{name: "duplicate marker field", policy: policy, audit: audit, marker: bytes.Replace(marker, []byte(`{"schemaVersion":1`), []byte(`{"schemaVersion":1,"schemaVersion":1`), 1), policyName: "policy.json", auditName: "audit.json"},
		{name: "trailing marker JSON", policy: policy, audit: audit, marker: append(append([]byte(nil), marker...), []byte(`{}`)...), policyName: "policy.json", auditName: "audit.json"},
		{name: "noncanonical marker whitespace", policy: policy, audit: audit, marker: append([]byte(" "), marker...), policyName: "policy.json", auditName: "audit.json"},
		{name: "policy hash mismatch", policy: append(append([]byte(nil), policy...), ' '), audit: audit, marker: marker, policyName: "policy.json", auditName: "audit.json"},
		{name: "audit hash mismatch", policy: policy, audit: append(append([]byte(nil), audit...), ' '), marker: marker, policyName: "policy.json", auditName: "audit.json"},
		{name: "policy name mismatch", policy: policy, audit: audit, marker: marker, policyName: "other.json", auditName: "audit.json"},
		{name: "path-like policy name", policy: policy, audit: audit, marker: marker, policyName: "dir/policy.json", auditName: "audit.json"},
		{name: "malformed policy", policy: []byte(`{}`), audit: audit, marker: mustBuildMarker(t, "policy.json", []byte(`{}`), "audit.json", audit), policyName: "policy.json", auditName: "audit.json"},
		{name: "malformed audit", policy: policy, audit: []byte(`{}`), marker: mustBuildMarker(t, "policy.json", policy, "audit.json", []byte(`{}`)), policyName: "policy.json", auditName: "audit.json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			committed, err := ValidateCommittedBundle(test.policyName, test.policy, test.auditName, test.audit, test.marker)
			if err == nil || len(committed.Policy) != 0 || len(committed.Audit) != 0 {
				t.Fatal("invalid committed bundle exposed bytes")
			}
			for _, secret := range []string{"dir/policy.json", string(test.policy), string(test.audit)} {
				if len(secret) > 0 && strings.Contains(err.Error(), secret) {
					t.Fatalf("validation error leaked bundle value: %q", err)
				}
			}
		})
	}
}

func validCommittedBundlePair(t *testing.T) ([]byte, []byte) {
	t.Helper()
	candidate := mustParseCandidate(t)
	fake := newFakeSigner(t)
	policy, audit, err := Sign(context.Background(), fake, candidate, validSignOptions(fake, fake.SPKISHA256))
	if err != nil {
		t.Fatal(err)
	}
	auditBytes, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	return policy.Policy, auditBytes
}

func mustBuildMarker(t testing.TB, policyName string, policy []byte, auditName string, audit []byte) []byte {
	t.Helper()
	marker, err := BuildBundleCommit(policyName, policy, auditName, audit)
	if err != nil {
		t.Fatal(err)
	}
	return marker
}

func decimalLength(value int) string {
	data, _ := json.Marshal(value)
	return string(data)
}
