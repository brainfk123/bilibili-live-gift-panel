package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy/bundlefs"
)

func TestImportBundleCreatesExactPrivateCommittedBundle(t *testing.T) {
	fixture := newImportBundleFixture(t)
	var output bytes.Buffer
	if err := run(context.Background(), fixture.args(), nil, nil, &output, nil); err != nil {
		t.Fatal(err)
	}
	if output.String() != "publisher policy bundle imported\n" {
		t.Fatalf("output = %q", output.String())
	}
	committed, err := bundlefs.ReadCommittedBundle(fixture.policyPath(), fixture.auditPath())
	if err != nil {
		t.Fatalf("imported bundle is not private and committed: %v", err)
	}
	if !bytes.Equal(committed.Policy, fixture.policy) || !bytes.Equal(committed.Audit, fixture.audit) {
		t.Fatal("import changed policy or audit bytes")
	}
	marker, err := os.ReadFile(filepath.Join(fixture.parent, "bundle", trustpolicy.BundleCommitFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(marker, fixture.commit) {
		t.Fatal("generated commit bytes differ from immutable downloaded commit")
	}
}

func TestImportBundleRejectsCommitSubstitutionBeforeCreatingPrivateParent(t *testing.T) {
	fixture := newImportBundleFixture(t)
	if err := os.WriteFile(fixture.commitSource, append(append([]byte(nil), fixture.commit...), ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), fixture.args(), nil, nil, &bytes.Buffer{}, nil); err == nil {
		t.Fatal("noncanonical immutable commit was accepted")
	}
	if _, err := os.Lstat(fixture.parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private parent exists after rejected import: %v", err)
	}
}

func TestImportedBundleRejectsExtraEntry(t *testing.T) {
	fixture := newImportBundleFixture(t)
	if err := run(context.Background(), fixture.args(), nil, nil, &bytes.Buffer{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.parent, "bundle", "extra.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	committed, err := bundlefs.ReadCommittedBundle(fixture.policyPath(), fixture.auditPath())
	if err == nil || len(committed.Policy) != 0 || len(committed.Audit) != 0 {
		t.Fatal("bundle with extra entry exposed trusted bytes")
	}
}

type importBundleFixture struct {
	parent, policySource, auditSource, commitSource string
	policy, audit, commit                           []byte
}

func newImportBundleFixture(t testing.TB) importBundleFixture {
	t.Helper()
	sources := t.TempDir()
	policy, audit := testBundlePayload(t, "credential-free-import")
	commit, err := trustpolicy.BuildBundleCommit("policy.json", policy, "audit.json", audit)
	if err != nil {
		t.Fatal(err)
	}
	fixture := importBundleFixture{
		parent:       filepath.Join(t.TempDir(), "private-bundle"),
		policySource: filepath.Join(sources, "policy.json"),
		auditSource:  filepath.Join(sources, "audit.json"),
		commitSource: filepath.Join(sources, "commit.json"),
		policy:       policy,
		audit:        audit,
		commit:       commit,
	}
	for path, contents := range map[string][]byte{fixture.policySource: policy, fixture.auditSource: audit, fixture.commitSource: commit} {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func (fixture importBundleFixture) args() []string {
	return []string{"import-bundle", "--policy-source", fixture.policySource, "--audit-source", fixture.auditSource, "--commit-source", fixture.commitSource, "--bundle-parent", fixture.parent}
}

func (fixture importBundleFixture) policyPath() string {
	return filepath.Join(fixture.parent, "bundle", "policy.json")
}

func (fixture importBundleFixture) auditPath() string {
	return filepath.Join(fixture.parent, "bundle", "audit.json")
}
