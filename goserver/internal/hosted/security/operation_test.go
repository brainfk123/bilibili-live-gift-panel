package security

import "testing"

func TestParseOperationPurposeAcceptsOnlyProtectedAdministratorOperations(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"bili_service_replace", "admin_email_change", "recovery_regenerate"} {
		purpose, ok := ParseOperationPurpose(value)
		if !ok || string(purpose) != value {
			t.Fatalf("ParseOperationPurpose(%q) = %q, %v", value, purpose, ok)
		}
	}
	for _, value := range []string{"", "obs_reissue", "BILI_SERVICE_REPLACE", "recovery_regenerate "} {
		if purpose, ok := ParseOperationPurpose(value); ok || purpose != "" {
			t.Fatalf("ParseOperationPurpose(%q) = %q, %v, want rejected", value, purpose, ok)
		}
	}
}

func TestValidOperationTargetRejectsEmptyAndOversizedValues(t *testing.T) {
	t.Parallel()

	if !ValidOperationTarget("global") || !ValidOperationTarget("sha256:0123456789abcdef") {
		t.Fatal("ValidOperationTarget rejected a bounded target")
	}
	if ValidOperationTarget("") || ValidOperationTarget(string(make([]byte, 257))) {
		t.Fatal("ValidOperationTarget accepted an empty or oversized target")
	}
}
