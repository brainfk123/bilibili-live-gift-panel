package main

import (
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy/bundlefs"
)

// ReadCommittedBundle remains as a CLI compatibility wrapper. Filesystem
// consumers such as Task 9 import bundlefs.ReadCommittedBundle directly.
func ReadCommittedBundle(policyPath, auditPath string) (trustpolicy.CommittedBundle, error) {
	return readCommittedBundle(policyPath, auditPath)
}

func readCommittedBundle(policyPath, auditPath string) (trustpolicy.CommittedBundle, error) {
	committed, err := bundlefs.ReadCommittedBundle(policyPath, auditPath)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errCommand
	}
	return committed, nil
}
