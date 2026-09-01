package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy/bundlefs"
)

func runImportBundle(args []string, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	var policySource, auditSource, commitSource, bundleParent string
	flags := flag.NewFlagSet("trustpolicy import-bundle", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&policySource, "policy-source", "", "bounded downloaded policy")
	flags.StringVar(&auditSource, "audit-source", "", "bounded downloaded audit")
	flags.StringVar(&commitSource, "commit-source", "", "immutable downloaded commit")
	flags.StringVar(&bundleParent, "bundle-parent", "", "create-only private parent")
	if hasRepeatedRegisteredFlag(args, flags) || flags.Parse(args) != nil || flags.NArg() != 0 ||
		!requiredFlagsPresent(flags, "policy-source", "audit-source", "commit-source", "bundle-parent") {
		return errCommand
	}
	policy, err := readReviewedPublicFile(policySource, maxCandidateBytes)
	if err != nil {
		return errCommand
	}
	audit, err := readReviewedPublicFile(auditSource, maxCommittedAuditImportBytes)
	if err != nil {
		return errCommand
	}
	downloadedCommit, err := readReviewedPublicFile(commitSource, maxImportedCommitBytes)
	if err != nil {
		return errCommand
	}
	generatedCommit, err := trustpolicy.BuildBundleCommit("policy.json", policy, "audit.json", audit)
	if err != nil || !bytes.Equal(generatedCommit, downloadedCommit) {
		return errCommand
	}
	parent, err := filepath.Abs(bundleParent)
	if err != nil || strings.TrimSpace(bundleParent) == "" {
		return errCommand
	}
	parent = filepath.Clean(parent)
	if _, err := os.Lstat(parent); !os.IsNotExist(err) {
		return errCommand
	}
	if err := bundlefs.CreatePrivateDirectory(parent); err != nil {
		return errCommand
	}
	bundle := filepath.Join(parent, "bundle")
	policyPath := filepath.Join(bundle, "policy.json")
	auditPath := filepath.Join(bundle, "audit.json")
	if err := bundlefs.WriteCommittedBundle(policyPath, policy, auditPath, audit); err != nil {
		return errCommand
	}
	committed, err := bundlefs.ReadCommittedBundle(policyPath, auditPath)
	if err != nil || !bytes.Equal(committed.Policy, policy) || !bytes.Equal(committed.Audit, audit) {
		return errCommand
	}
	committedMarker, err := trustpolicy.BuildBundleCommit("policy.json", committed.Policy, "audit.json", committed.Audit)
	if err != nil || !bytes.Equal(committedMarker, downloadedCommit) {
		return errCommand
	}
	if written, err := fmt.Fprintln(output, "publisher policy bundle imported"); err != nil || written != len("publisher policy bundle imported\n") {
		return errCommand
	}
	return nil
}

const maxCommittedAuditImportBytes = 64 << 10
