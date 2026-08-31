package trustpolicy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var candidateValidationTime = time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)

func TestPolicyCandidateCanonicalBytesMatchClientGolden(t *testing.T) {
	// Mutation caught: changing a field name, field order, or algorithm-specific
	// signer schema would produce bytes the Task 1 client does not verify.
	candidate, err := ParseCandidate(readCandidateFixture(t), CandidateOptions{
		ExpectedPreviousEpoch: 0,
		Now:                   candidateValidationTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := CanonicalSigned(candidate)
	if err != nil {
		t.Fatal(err)
	}

	var clientFixture struct {
		Signed json.RawMessage `json:"signed"`
	}
	clientBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "goserver", "testdata", "update-trust", "policy-epoch-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(clientBytes, &clientFixture); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, clientFixture.Signed) {
		t.Fatalf("canonical candidate differs from Task 1 client golden\ngot:  %s\nwant: %s", got, clientFixture.Signed)
	}
}

func TestPolicyCandidateRejectsWrongEpochAndNonCanonicalEnvelope(t *testing.T) {
	valid := string(readCandidateFixture(t))
	tests := []struct {
		name             string
		body             string
		expectedPrevious uint64
	}{
		{name: "not next epoch", body: valid, expectedPrevious: 1},
		{name: "skips epoch", body: strings.Replace(valid, `"epoch":1`, `"epoch":2`, 1), expectedPrevious: 0},
		{name: "wrapped envelope", body: `{"signed":` + valid + `}`, expectedPrevious: 0},
		{name: "unknown field", body: strings.Replace(valid, `"epoch":1`, `"epoch":1,"schemaVersion":1`, 1), expectedPrevious: 0},
		{name: "duplicate key", body: strings.Replace(valid, `"epoch":1`, `"epoch":1,"epoch":1`, 1), expectedPrevious: 0},
		{name: "trailing value", body: valid + `{}`, expectedPrevious: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseCandidate([]byte(test.body), CandidateOptions{ExpectedPreviousEpoch: test.expectedPrevious, Now: candidateValidationTime}); err == nil {
				t.Fatal("ParseCandidate() error = nil, want rejection")
			}
		})
	}
}

func TestPolicyCandidateRestrictsPublishersTimeTagsAndHashes(t *testing.T) {
	valid := string(readCandidateFixture(t))
	tests := []struct {
		name string
		body string
		now  time.Time
	}{
		{name: "expired", body: valid, now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "non UTC expiry", body: strings.Replace(valid, "2030-01-01T00:00:00Z", "2030-01-01T08:00:00+08:00", 1), now: candidateValidationTime},
		{name: "arbitrary stable publisher", body: strings.Replace(valid, "NaisNet Technology Co., Ltd.", "Example Technology Co., Ltd.", 1), now: candidateValidationTime},
		{name: "NaisNet country is exact", body: strings.Replace(valid, `"country":"CN"`, `"country":"US"`, 1), now: candidateValidationTime},
		{name: "NaisNet organization ID is exact", body: strings.Replace(valid, "91210103MA7CJ3C094", "91210103MA7CJ3C095", 1), now: candidateValidationTime},
		{name: "NaisNet cannot bridge", body: strings.Replace(valid, `"allowedChannel":"stable"`, `"allowedChannel":"legacy-rushrush"`, 1), now: candidateValidationTime},
		{name: "NaisNet role is client primary", body: strings.Replace(valid, `"role":"primary"`, `"role":"stable"`, 1), now: candidateValidationTime},
		{name: "NaisNet cannot claim reserved bridge tag", body: strings.Replace(valid, `"v0.4.12"`, `"v0.4.11"`, 1), now: candidateValidationTime},
		{name: "RushRush cannot sign stable", body: strings.Replace(valid, `"allowedChannel":"legacy-rushrush"`, `"allowedChannel":"stable"`, 1), now: candidateValidationTime},
		{name: "RushRush tag is exact", body: strings.Replace(valid, `"v0.4.11"`, `"v0.4.12"`, 1), now: candidateValidationTime},
		{name: "RushRush identity is exact", body: strings.Replace(valid, "91450900MADM3GLG5P", "91450900MADM3GLG5Q", 1), now: candidateValidationTime},
		{name: "noncanonical tag", body: strings.Replace(valid, `"v0.4.12"`, `"v0.04.12"`, 1), now: candidateValidationTime},
		{name: "duplicate stable tag", body: strings.Replace(valid, `"allowedTags":["v0.4.12"]`, `"allowedTags":["v0.4.12","v0.4.12"]`, 1), now: candidateValidationTime},
		{name: "stable tags require canonical order", body: strings.Replace(valid, `"allowedTags":["v0.4.12"]`, `"allowedTags":["v0.4.13","v0.4.12"]`, 1), now: candidateValidationTime},
		{name: "uppercase manifest hash", body: strings.Replace(valid, `"allowedTags":["v0.4.12"]`, `"allowedTags":["v0.4.12"],"manifestSha256":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"`, 1), now: candidateValidationTime},
		{name: "malformed manifest hash", body: strings.Replace(valid, `"allowedTags":["v0.4.12"]`, `"allowedTags":["v0.4.12"],"manifestSha256":"abc"`, 1), now: candidateValidationTime},
		{name: "duplicate ID", body: strings.Replace(valid, `"id":"rushrush-bridge"`, `"id":"naisnet-primary"`, 1), now: candidateValidationTime},
		{name: "publisher order is canonical", body: swapPublisherOrder(valid), now: candidateValidationTime},
		{name: "arbitrary third publisher", body: strings.TrimSuffix(valid, `]}`) + `,{"id":"extra","role":"primary","country":"CN","organization":"NaisNet Technology Co., Ltd.","organizationId":"91210103MA7CJ3C094","allowedChannel":"stable","allowedTags":["v0.4.13"]}]}`, now: candidateValidationTime},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseCandidate([]byte(test.body), CandidateOptions{ExpectedPreviousEpoch: 0, Now: test.now}); err == nil {
				t.Fatal("ParseCandidate() error = nil, want rejection")
			}
		})
	}
}

func TestPolicyCandidateAllowsCanonicalLowercaseManifestHash(t *testing.T) {
	valid := string(readCandidateFixture(t))
	withHash := strings.Replace(valid, `"allowedTags":["v0.4.12"]`, `"allowedTags":["v0.4.12"],"manifestSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, 1)
	if _, err := ParseCandidate([]byte(withHash), CandidateOptions{ExpectedPreviousEpoch: 0, Now: candidateValidationTime}); err != nil {
		t.Fatalf("ParseCandidate() rejected canonical manifest hash: %v", err)
	}
}

func TestPolicyCandidateAllowsNaisNetOnlyAfterBridgeRetirement(t *testing.T) {
	valid := string(readCandidateFixture(t))
	bridgeStart := strings.Index(valid, `,{"id":"rushrush-bridge"`)
	if bridgeStart < 0 {
		t.Fatal("fixture bridge entry not found")
	}
	withoutBridge := valid[:bridgeStart] + `]}`
	if _, err := ParseCandidate([]byte(withoutBridge), CandidateOptions{ExpectedPreviousEpoch: 0, Now: candidateValidationTime}); err != nil {
		t.Fatalf("ParseCandidate() rejected NaisNet-only policy: %v", err)
	}
}

func readCandidateFixture(t testing.TB) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json"))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(data)
}

func swapPublisherOrder(valid string) string {
	var document struct {
		Epoch      uint64            `json:"epoch"`
		ExpiresAt  string            `json:"expiresAt"`
		Publishers []json.RawMessage `json:"publishers"`
	}
	if err := json.Unmarshal([]byte(valid), &document); err != nil || len(document.Publishers) != 2 {
		return valid
	}
	document.Publishers[0], document.Publishers[1] = document.Publishers[1], document.Publishers[0]
	data, err := json.Marshal(document)
	if err != nil {
		return valid
	}
	return string(data)
}
