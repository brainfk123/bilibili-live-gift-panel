package bundlefs_test

import (
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy/bundlefs"
)

func TestTask9CanImportFilesystemBundleReader(t *testing.T) {
	var reader func(string, string) (trustpolicy.CommittedBundle, error) = bundlefs.ReadCommittedBundle
	if reader == nil {
		t.Fatal("filesystem bundle reader is not callable")
	}
}
