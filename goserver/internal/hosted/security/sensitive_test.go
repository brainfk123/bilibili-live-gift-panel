package security

import "testing"

func TestSensitiveSessionFenceRejectsInvalidIdentityAndPreservesExactFence(t *testing.T) {
	issuer := NewSensitiveSessionIssuer()
	if issuer == nil {
		t.Fatal("NewSensitiveSessionIssuer() returned nil")
	}
	for _, test := range []struct {
		name            string
		sessionID       int64
		credentialEpoch int64
		wantValid       bool
	}{
		{name: "exact fence", sessionID: 17, credentialEpoch: 4, wantValid: true},
		{name: "missing session", credentialEpoch: 4},
		{name: "missing epoch", sessionID: 17},
		{name: "negative session", sessionID: -1, credentialEpoch: 4},
		{name: "negative epoch", sessionID: 17, credentialEpoch: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fence, valid := issuer.Issue(test.sessionID, test.credentialEpoch)
			if valid != test.wantValid {
				t.Fatalf("Issue() valid = %t, want %t", valid, test.wantValid)
			}
			gotID, gotEpoch, opened := issuer.Open(fence)
			if opened != test.wantValid {
				t.Fatalf("Open() valid = %t, want %t", opened, test.wantValid)
			}
			if test.wantValid && (gotID != test.sessionID || gotEpoch != test.credentialEpoch) {
				t.Fatalf("Open() = (%d, %d), want (%d, %d)", gotID, gotEpoch, test.sessionID, test.credentialEpoch)
			}
		})
	}

	if _, _, valid := issuer.Open(SensitiveSession{}); valid {
		t.Fatal("zero SensitiveSession unexpectedly opened")
	}
	fence, valid := issuer.Issue(17, 4)
	if !valid {
		t.Fatal("exact fence is invalid")
	}
	if _, _, valid := NewSensitiveSessionIssuer().Open(fence); valid {
		t.Fatal("foreign issuer unexpectedly opened fence")
	}
}
