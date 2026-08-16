package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/security"
)

func TestServicePollPendingKeepsChallengeAlive(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 0, 0, 0, time.UTC)
	verifier := &memoryVerifier{
		challenge: Challenge{ID: "challenge-pending", QRImage: "data:image/png;base64,qr", ExpiresAt: now.Add(5 * time.Minute)},
		pollErrs:  []error{ErrVerificationPending, ErrVerificationPending},
	}
	service := newTestService(t, &memoryRepository{}, verifier, now)

	challenge, err := service.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if challenge.ID != "challenge-pending" || challenge.QRImage == "" {
		t.Fatalf("Begin() = %#v, want public challenge", challenge)
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := service.Poll(context.Background(), challenge.ID)
		if err != nil {
			t.Fatalf("Poll() attempt %d error = %v", attempt+1, err)
		}
		if result.Status != ChallengePending || result.RegistrationIntent != "" {
			t.Fatalf("Poll() attempt %d = %#v, want pending without registration secret", attempt+1, result)
		}
	}
	if got := verifier.forgotten(); len(got) != 0 {
		t.Fatalf("pending verification called Forget: %v", got)
	}
}

func TestServiceExistingAccountLoginUsesOnlyHashedSiteToken(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 10, 0, 0, time.UTC)
	verifier := &memoryVerifier{
		challenge:     Challenge{ID: "challenge-existing", QRImage: "data:image/png;base64,qr", ExpiresAt: now.Add(5 * time.Minute)},
		verifications: []Verification{{UID: "32249588", CompletedAt: now}},
	}
	repository := &memoryRepository{account: Account{ID: 41, CredentialEpoch: 3}}
	service := newTestService(t, repository, verifier, now)
	challenge, err := service.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Poll(context.Background(), challenge.ID)
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if result.Status != ChallengeVerified || result.RegistrationIntent != "" {
		t.Fatalf("Poll() = %#v, want verified existing account", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "32249588") {
		t.Fatalf("Poll() exposed UID: %s", encoded)
	}
	assertForgottenExactly(t, verifier, challenge.ID)

	login, err := service.Login(context.Background(), challenge.ID)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if login.Token == "" || login.AccountID != 41 || !login.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("Login() = %#v, want site session", login)
	}
	if len(repository.createdSessions) != 1 {
		t.Fatalf("CreateSession calls = %d, want 1", len(repository.createdSessions))
	}
	persisted := repository.createdSessions[0]
	if persisted.AccountID != 41 || persisted.CredentialEpoch != 3 || len(persisted.TokenHash) != 32 {
		t.Fatalf("persisted session = %#v", persisted)
	}
	if bytes.Contains(persisted.TokenHash, []byte(login.Token)) || repository.containsPlaintext(login.Token) {
		t.Fatalf("repository observed plaintext site token %q", login.Token)
	}

	if _, err := service.Login(context.Background(), challenge.ID); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("second Login() error = %v, want single-use ErrAuthenticationFailed", err)
	}
}

func TestServiceConsumeAccountProofIsBoundRecentAndOneShot(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 15, 0, 0, time.UTC)
	for _, test := range []struct {
		name        string
		accountID   int64
		completedAt time.Time
		want        error
	}{
		{name: "matching recent account", accountID: 41, completedAt: now, want: nil},
		{name: "mismatched account", accountID: 42, completedAt: now, want: ErrAuthenticationFailed},
		{name: "expired completion", accountID: 41, completedAt: now, want: ErrAuthenticationFailed},
		{name: "future completion", accountID: 41, completedAt: now.Add(30 * time.Second), want: ErrAuthenticationFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := &memoryVerifier{
				challenge:     Challenge{ID: "proof-" + test.name, QRImage: "qr", ExpiresAt: now.Add(5 * time.Minute)},
				verifications: []Verification{{UID: "32249588", CompletedAt: test.completedAt}},
			}
			service := newTestService(t, &memoryRepository{account: Account{ID: 41, CredentialEpoch: 3}}, verifier, now)
			challenge, err := service.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if result, err := service.Poll(context.Background(), challenge.ID); err != nil || result.Status != ChallengeVerified {
				t.Fatalf("Poll() = %#v, %v", result, err)
			}
			if test.name == "expired completion" {
				service.mu.Lock()
				service.challenges[challenge.ID].completedAt = now.Add(-15*time.Minute - time.Nanosecond)
				service.mu.Unlock()
			}

			err = service.ConsumeAccountProof(context.Background(), challenge.ID, test.accountID, 15*time.Minute)
			if !errors.Is(err, test.want) {
				t.Fatalf("ConsumeAccountProof() error = %v, want %v", err, test.want)
			}
			if err := service.ConsumeAccountProof(context.Background(), challenge.ID, 41, 15*time.Minute); !errors.Is(err, ErrAuthenticationFailed) {
				t.Fatalf("reused proof error = %v, want terminal rejection", err)
			}
			service.mu.Lock()
			_, present := service.challenges[challenge.ID]
			service.mu.Unlock()
			if present {
				t.Fatal("terminal account proof remained reusable")
			}
		})
	}
}

func TestServiceConsumeAccountProofLeavesPendingChallengeRetryable(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 16, 0, 0, time.UTC)
	verifier := &memoryVerifier{challenge: Challenge{ID: "proof-pending", QRImage: "qr", ExpiresAt: now.Add(time.Minute)}}
	service := newTestService(t, &memoryRepository{}, verifier, now)
	challenge, err := service.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConsumeAccountProof(context.Background(), challenge.ID, 7, 15*time.Minute); !errors.Is(err, ErrVerificationPending) {
		t.Fatalf("ConsumeAccountProof() error = %v", err)
	}
	service.mu.Lock()
	_, present := service.challenges[challenge.ID]
	service.mu.Unlock()
	if !present {
		t.Fatal("pending proof was consumed")
	}
}

func TestServiceUnknownAccountReturnsSingleUseRegistrationIntent(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 20, 0, 0, time.UTC)
	verifier := &memoryVerifier{
		challenge:     Challenge{ID: "challenge-new", QRImage: "data:image/png;base64,qr", ExpiresAt: now.Add(5 * time.Minute)},
		verifications: []Verification{{UID: "90000001", CompletedAt: now}},
	}
	repository := &memoryRepository{findErr: ErrNotFound}
	keys := fixedServiceKeyring(t)
	service, err := NewService(repository, keys, verifier, ServiceOptions{
		Now: nowFunc(now), ChallengeTTL: 5 * time.Minute, RegistrationTTL: 5 * time.Minute, SessionTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Poll(context.Background(), challenge.ID)
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if result.Status != RegistrationRequired || len(result.RegistrationIntent) < 32 || !result.ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("Poll() = %#v, want short-lived registration intent", result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "90000001") {
		t.Fatalf("registration response exposed UID: %s", encoded)
	}
	if len(repository.createdAccounts) != 0 || len(repository.createdSessions) != 0 {
		t.Fatalf("unknown account was bound or logged in early: accounts=%d sessions=%d", len(repository.createdAccounts), len(repository.createdSessions))
	}
	assertForgottenExactly(t, verifier, challenge.ID)

	verifiedUID, err := service.ConsumeRegistrationIntent(result.RegistrationIntent)
	if err != nil {
		t.Fatalf("ConsumeRegistrationIntent() error = %v", err)
	}
	plaintext, err := keys.Open("bili_uid", verifiedUID.Ciphertext)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if string(plaintext) != "90000001" || len(verifiedUID.Lookup) != 32 {
		t.Fatalf("consumed registration identity = %#v plaintext=%q", verifiedUID, plaintext)
	}
	if _, err := service.ConsumeRegistrationIntent(result.RegistrationIntent); !errors.Is(err, ErrRegistrationIntentInvalid) {
		t.Fatalf("second ConsumeRegistrationIntent() error = %v, want single-use rejection", err)
	}
	if _, err := service.ConsumeRegistrationIntent(result.RegistrationIntent + "forged"); !errors.Is(err, ErrRegistrationIntentInvalid) {
		t.Fatalf("forged ConsumeRegistrationIntent() error = %v", err)
	}
}

func TestServiceRegistrationIntentExpires(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 30, 0, 0, time.UTC)
	clock := now
	verifier := &memoryVerifier{
		challenge:     Challenge{ID: "challenge-expiring-registration", QRImage: "data:image/png;base64,qr", ExpiresAt: now.Add(5 * time.Minute)},
		verifications: []Verification{{UID: "90000002", CompletedAt: now}},
	}
	service, err := NewService(&memoryRepository{findErr: ErrNotFound}, fixedServiceKeyring(t), verifier, ServiceOptions{
		Now: func() time.Time { return clock }, ChallengeTTL: 5 * time.Minute, RegistrationTTL: time.Minute, SessionTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, _ := service.Begin(context.Background())
	result, err := service.Poll(context.Background(), challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock = now.Add(time.Minute)
	if _, err := service.ConsumeRegistrationIntent(result.RegistrationIntent); !errors.Is(err, ErrRegistrationIntentInvalid) {
		t.Fatalf("expired ConsumeRegistrationIntent() error = %v", err)
	}
}

func TestServiceRegistrationIntentReservationHasOneConcurrentWinner(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 32, 0, 0, time.UTC)
	service, token, keys := newRegistrationIntentForTest(t, now, 5*time.Minute)

	const contenders = 16
	start := make(chan struct{})
	results := make(chan RegistrationIntentReservation, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			reservation, err := service.ReserveRegistrationIntent(token)
			if err == nil {
				results <- reservation
			}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	var winner RegistrationIntentReservation
	for reservation := range results {
		if winner != nil {
			t.Fatal("more than one concurrent registration reservation succeeded")
		}
		winner = reservation
	}
	if winner == nil || !winner.Valid() {
		t.Fatalf("winning reservation = %#v", winner)
	}
	uid, expiresAt, ok := winner.Identity()
	if !ok || !expiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("Identity() expiry=%v ok=%v", expiresAt, ok)
	}
	plaintext, err := keys.Open("bili_uid", uid.Ciphertext)
	if err != nil || string(plaintext) != "90000020" || len(uid.Lookup) != 32 {
		t.Fatalf("reserved identity plaintext=%q lookup=%d error=%v", plaintext, len(uid.Lookup), err)
	}
	winner.Abort()
}

func TestServiceRegistrationIntentAbortRestoresUntilAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 34, 0, 0, time.UTC)
	clock := &mutableServiceClock{value: now}
	service, token, _ := newRegistrationIntentForClockTest(t, clock, 5*time.Minute)

	first, err := service.ReserveRegistrationIntent(token)
	if err != nil {
		t.Fatal(err)
	}
	first.Abort()
	second, err := service.ReserveRegistrationIntent(token)
	if err != nil {
		t.Fatalf("ReserveRegistrationIntent() after Abort error = %v", err)
	}
	_, expiresAt, ok := second.Identity()
	if !ok {
		t.Fatal("second reservation did not expose its identity")
	}
	clock.Set(expiresAt)
	if second.Valid() {
		t.Fatal("reservation remained valid at its absolute expiry")
	}
	second.Abort()
	if _, err := service.ReserveRegistrationIntent(token); !errors.Is(err, ErrRegistrationIntentInvalid) {
		t.Fatalf("ReserveRegistrationIntent() after expiry error = %v", err)
	}
}

func TestServiceRegistrationIntentCommitIsIrreversible(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 36, 0, 0, time.UTC)
	service, token, _ := newRegistrationIntentForTest(t, now, 5*time.Minute)
	reservation, err := service.ReserveRegistrationIntent(token)
	if err != nil {
		t.Fatal(err)
	}
	reservation.Commit()
	reservation.Abort()
	if reservation.Valid() {
		t.Fatal("committed reservation remained valid")
	}
	if _, err := service.ReserveRegistrationIntent(token); !errors.Is(err, ErrRegistrationIntentInvalid) {
		t.Fatalf("ReserveRegistrationIntent() after Commit error = %v", err)
	}
}

func TestServiceAbandonedReservedRegistrationIsDestroyedOnTimerAndClose(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 38, 0, 0, time.UTC)
	t.Run("timer", func(t *testing.T) {
		service, token, _ := newRegistrationIntentForTest(t, now, 25*time.Millisecond)
		reservation, err := service.ReserveRegistrationIntent(token)
		if err != nil {
			t.Fatal(err)
		}
		waitForServiceState(t, func() bool {
			service.mu.Lock()
			defer service.mu.Unlock()
			return len(service.registrations) == 0
		})
		if uid, _, ok := reservation.Identity(); reservation.Valid() || ok || len(uid.Ciphertext) != 0 || len(uid.Lookup) != 0 {
			t.Fatalf("expired reservation retained identity: %#v", uid)
		}
	})

	t.Run("close", func(t *testing.T) {
		service, token, _ := newRegistrationIntentForTest(t, now, time.Minute)
		reservation, err := service.ReserveRegistrationIntent(token)
		if err != nil {
			t.Fatal(err)
		}
		service.Close()
		if uid, _, ok := reservation.Identity(); reservation.Valid() || ok || len(uid.Ciphertext) != 0 || len(uid.Lookup) != 0 {
			t.Fatalf("closed reservation retained identity: %#v", uid)
		}
	})
}

func TestServiceForgetsEveryTerminalCancelledAndShutdownChallenge(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 40, 0, 0, time.UTC)
	tests := []struct {
		name       string
		pollErr    error
		findErr    error
		account    Account
		wantPoll   error
		cancelOnly bool
	}{
		{name: "expired", pollErr: ErrChallengeExpired, wantPoll: ErrChallengeExpired},
		{name: "terminal failure", pollErr: ErrVerificationFailed, wantPoll: ErrAuthenticationFailed},
		{name: "duplicate uid repository result", findErr: ErrUIDAlreadyBound, wantPoll: ErrAuthenticationFailed},
		{name: "disabled account", account: Account{ID: 7, CredentialEpoch: 1, DisabledAt: pointerTime(now)}, wantPoll: ErrAuthenticationFailed},
		{name: "cancelled", cancelOnly: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifier := &memoryVerifier{
				challenge:     Challenge{ID: "challenge-" + strings.ReplaceAll(test.name, " ", "-"), QRImage: "qr", ExpiresAt: now.Add(5 * time.Minute)},
				verifications: []Verification{{UID: "777", CompletedAt: now}}, pollErrs: []error{test.pollErr},
			}
			repository := &memoryRepository{account: test.account, findErr: test.findErr}
			if repository.account.ID == 0 && repository.findErr == nil {
				repository.account = Account{ID: 8, CredentialEpoch: 1}
			}
			service := newTestService(t, repository, verifier, now)
			challenge, err := service.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if test.cancelOnly {
				service.Cancel(challenge.ID)
			} else {
				_, err = service.Poll(context.Background(), challenge.ID)
				if !errors.Is(err, test.wantPoll) {
					t.Fatalf("Poll() error = %v, want %v", err, test.wantPoll)
				}
			}
			assertForgottenExactly(t, verifier, challenge.ID)
		})
	}

	verifier := &memoryVerifier{challenge: Challenge{ID: "shutdown-challenge", QRImage: "qr", ExpiresAt: now.Add(5 * time.Minute)}}
	service := newTestService(t, &memoryRepository{}, verifier, now)
	challenge, err := service.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	service.Close()
	assertForgottenExactly(t, verifier, challenge.ID)
}

func TestServiceCancelIsIdempotentAndDoesNotRevealChallengeExistence(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 50, 0, 0, time.UTC)
	verifier := &memoryVerifier{challenge: Challenge{ID: "idempotent-cancel", QRImage: "qr", ExpiresAt: now.Add(time.Minute)}}
	service := newTestService(t, &memoryRepository{}, verifier, now)
	challenge, err := service.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	service.Cancel(challenge.ID)
	service.Cancel(challenge.ID)
	service.Cancel("unknown-challenge")
	assertForgottenExactly(t, verifier, challenge.ID)
}

func TestServiceRequireSessionAndLogoutHashCallerToken(t *testing.T) {
	now := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	repository := &memoryRepository{session: Session{ID: 3, AccountID: 55, CredentialEpoch: 2, ExpiresAt: now.Add(time.Hour)}}
	service := newTestService(t, repository, &memoryVerifier{}, now)

	session, err := service.RequireSession(context.Background(), "site-token-value")
	if err != nil {
		t.Fatalf("RequireSession() error = %v", err)
	}
	if session.AccountID != 55 || len(repository.findSessionHashes) != 1 || len(repository.findSessionHashes[0]) != 32 {
		t.Fatalf("RequireSession() session=%#v hashes=%v", session, repository.findSessionHashes)
	}
	if repository.containsPlaintext("site-token-value") {
		t.Fatal("repository observed plaintext token during RequireSession")
	}
	if err := service.Logout(context.Background(), "site-token-value"); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if len(repository.revokedHashes) != 1 || len(repository.revokedHashes[0]) != 32 || repository.containsPlaintext("site-token-value") {
		t.Fatalf("Logout() hashes = %v, plaintext token reached repository", repository.revokedHashes)
	}
}

func TestServiceCancellationDuringUIDLookupCannotMintRegistrationIntent(t *testing.T) {
	now := time.Date(2026, 8, 16, 9, 10, 0, 0, time.UTC)
	verifier := &memoryVerifier{
		challenge:     Challenge{ID: "challenge-cancel-during-lookup", QRImage: "qr", ExpiresAt: now.Add(5 * time.Minute)},
		verifications: []Verification{{UID: "90000003", CompletedAt: now}},
	}
	repository := &blockingLookupRepository{
		memoryRepository: memoryRepository{findErr: ErrNotFound},
		started:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	service := newTestService(t, repository, verifier, now)
	challenge, err := service.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	type pollOutcome struct {
		result PollResult
		err    error
	}
	outcomes := make(chan pollOutcome, 1)
	go func() {
		result, err := service.Poll(context.Background(), challenge.ID)
		outcomes <- pollOutcome{result: result, err: err}
	}()
	<-repository.started
	service.Cancel(challenge.ID)
	close(repository.release)
	outcome := <-outcomes
	if !errors.Is(outcome.err, ErrAuthenticationFailed) {
		t.Fatalf("Poll() after concurrent Cancel = %#v, %v; want no registration intent", outcome.result, outcome.err)
	}
	if outcome.result.RegistrationIntent != "" {
		t.Fatalf("concurrent Cancel minted registration intent %q", outcome.result.RegistrationIntent)
	}
	assertForgottenExactly(t, verifier, challenge.ID)
}

func TestServiceActivelyDeletesAbandonedChallengeAndRegistrationIntentAtTTL(t *testing.T) {
	now := time.Date(2026, 8, 16, 9, 20, 0, 0, time.UTC)
	t.Run("challenge", func(t *testing.T) {
		verifier := &memoryVerifier{challenge: Challenge{ID: "abandoned-challenge", QRImage: "qr", ExpiresAt: now.Add(time.Hour)}}
		service, err := NewService(&memoryRepository{}, fixedServiceKeyring(t), verifier, ServiceOptions{
			Now: nowFunc(now), ChallengeTTL: 25 * time.Millisecond, RegistrationTTL: 25 * time.Millisecond, SessionTTL: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		challenge, err := service.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		waitForServiceState(t, func() bool {
			service.mu.Lock()
			defer service.mu.Unlock()
			return len(service.challenges) == 0
		})
		assertForgottenExactly(t, verifier, challenge.ID)
	})

	t.Run("registration intent", func(t *testing.T) {
		verifier := &memoryVerifier{
			challenge:     Challenge{ID: "abandoned-registration", QRImage: "qr", ExpiresAt: now.Add(time.Hour)},
			verifications: []Verification{{UID: "90000004", CompletedAt: now}},
		}
		service, err := NewService(&memoryRepository{findErr: ErrNotFound}, fixedServiceKeyring(t), verifier, ServiceOptions{
			Now: nowFunc(now), ChallengeTTL: time.Second, RegistrationTTL: 25 * time.Millisecond, SessionTTL: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		challenge, err := service.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Poll(context.Background(), challenge.ID)
		if err != nil {
			t.Fatal(err)
		}
		waitForServiceState(t, func() bool {
			service.mu.Lock()
			defer service.mu.Unlock()
			return len(service.registrations) == 0
		})
		if _, err := service.ConsumeRegistrationIntent(result.RegistrationIntent); !errors.Is(err, ErrRegistrationIntentInvalid) {
			t.Fatalf("ConsumeRegistrationIntent() after physical TTL cleanup error = %v", err)
		}
	})
}

func TestServicePollCompletionAfterLogicalExpiryCannotCreateGrantOrIntent(t *testing.T) {
	initial := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name       string
		repository memoryRepository
	}{
		{name: "existing account", repository: memoryRepository{account: Account{ID: 91, CredentialEpoch: 1}}},
		{name: "unknown account", repository: memoryRepository{findErr: ErrNotFound}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := &mutableServiceClock{value: initial}
			verifier := &memoryVerifier{
				challenge:     Challenge{ID: "logical-expiry-" + strings.ReplaceAll(test.name, " ", "-"), QRImage: "qr", ExpiresAt: initial.Add(time.Hour)},
				verifications: []Verification{{UID: "90000005", CompletedAt: initial}},
			}
			repository := &blockingLookupRepository{
				memoryRepository: test.repository,
				started:          make(chan struct{}),
				release:          make(chan struct{}),
			}
			service, err := NewService(repository, fixedServiceKeyring(t), verifier, ServiceOptions{
				Now: clock.Now, ChallengeTTL: time.Second, RegistrationTTL: time.Second, SessionTTL: time.Hour,
			})
			if err != nil {
				t.Fatal(err)
			}
			challenge, err := service.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			type pollOutcome struct {
				result PollResult
				err    error
			}
			outcomes := make(chan pollOutcome, 1)
			go func() {
				result, err := service.Poll(context.Background(), challenge.ID)
				outcomes <- pollOutcome{result: result, err: err}
			}()
			<-repository.started
			clock.Set(initial.Add(2 * time.Second))
			close(repository.release)
			outcome := <-outcomes
			if !errors.Is(outcome.err, ErrChallengeExpired) || outcome.result.Status != "" || outcome.result.RegistrationIntent != "" {
				t.Fatalf("Poll() after logical expiry = %#v, %v", outcome.result, outcome.err)
			}
			assertForgottenExactly(t, verifier, challenge.ID)
		})
	}
}

type memoryVerifier struct {
	mu            sync.Mutex
	challenge     Challenge
	beginErr      error
	verifications []Verification
	pollErrs      []error
	polls         int
	forgetCalls   []string
}

func (verifier *memoryVerifier) Begin(context.Context) (Challenge, error) {
	return verifier.challenge, verifier.beginErr
}

func (verifier *memoryVerifier) Poll(context.Context, string) (Verification, error) {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	index := verifier.polls
	verifier.polls++
	var verification Verification
	if index < len(verifier.verifications) {
		verification = verifier.verifications[index]
	}
	if index < len(verifier.pollErrs) {
		return verification, verifier.pollErrs[index]
	}
	return verification, nil
}

func (verifier *memoryVerifier) Forget(challengeID string) {
	verifier.mu.Lock()
	verifier.forgetCalls = append(verifier.forgetCalls, challengeID)
	verifier.mu.Unlock()
}

func (verifier *memoryVerifier) forgotten() []string {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	return append([]string(nil), verifier.forgetCalls...)
}

type memoryRepository struct {
	account           Account
	findErr           error
	createAccountErr  error
	createSessionErr  error
	session           Session
	findSessionErr    error
	revokeErr         error
	createdAccounts   []EncryptedUID
	createdSessions   []Session
	findSessionHashes [][]byte
	revokedHashes     [][]byte
}

type blockingLookupRepository struct {
	memoryRepository
	started chan struct{}
	release chan struct{}
}

func (repository *blockingLookupRepository) FindAccountByUIDLookup(ctx context.Context, lookup []byte) (Account, error) {
	close(repository.started)
	select {
	case <-repository.release:
		return repository.memoryRepository.FindAccountByUIDLookup(ctx, lookup)
	case <-ctx.Done():
		return Account{}, ctx.Err()
	}
}

func (repository *memoryRepository) FindAccountByUIDLookup(_ context.Context, lookup []byte) (Account, error) {
	return repository.account, repository.findErr
}

func (repository *memoryRepository) CreateBoundAccount(_ context.Context, uid EncryptedUID) (Account, error) {
	repository.createdAccounts = append(repository.createdAccounts, EncryptedUID{Ciphertext: bytes.Clone(uid.Ciphertext), Lookup: bytes.Clone(uid.Lookup)})
	return repository.account, repository.createAccountErr
}

func (repository *memoryRepository) CreateSession(_ context.Context, session Session) error {
	copy := session
	copy.TokenHash = bytes.Clone(session.TokenHash)
	repository.createdSessions = append(repository.createdSessions, copy)
	return repository.createSessionErr
}

func (repository *memoryRepository) FindSessionByHash(_ context.Context, hash []byte, _ time.Time) (Session, error) {
	repository.findSessionHashes = append(repository.findSessionHashes, bytes.Clone(hash))
	return repository.session, repository.findSessionErr
}

func (repository *memoryRepository) RevokeSession(_ context.Context, hash []byte) error {
	repository.revokedHashes = append(repository.revokedHashes, bytes.Clone(hash))
	return repository.revokeErr
}

func (repository *memoryRepository) containsPlaintext(value string) bool {
	for _, session := range repository.createdSessions {
		if bytes.Contains(session.TokenHash, []byte(value)) {
			return true
		}
	}
	for _, hash := range append(append([][]byte(nil), repository.findSessionHashes...), repository.revokedHashes...) {
		if bytes.Contains(hash, []byte(value)) {
			return true
		}
	}
	return false
}

func newTestService(t *testing.T, repository Repository, verifier BiliVerifier, now time.Time) *Service {
	t.Helper()
	service, err := NewService(repository, fixedServiceKeyring(t), verifier, ServiceOptions{
		Now: nowFunc(now), ChallengeTTL: 5 * time.Minute, RegistrationTTL: 5 * time.Minute, SessionTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func newRegistrationIntentForTest(t *testing.T, now time.Time, ttl time.Duration) (*Service, string, security.Keyring) {
	t.Helper()
	return newRegistrationIntentForClockTest(t, &mutableServiceClock{value: now}, ttl)
}

func newRegistrationIntentForClockTest(t *testing.T, clock *mutableServiceClock, ttl time.Duration) (*Service, string, security.Keyring) {
	t.Helper()
	keys := fixedServiceKeyring(t)
	verifier := &memoryVerifier{
		challenge:     Challenge{ID: "reservation-challenge", QRImage: "qr", ExpiresAt: clock.Now().Add(5 * time.Minute)},
		verifications: []Verification{{UID: "90000020", CompletedAt: clock.Now()}},
	}
	service, err := NewService(&memoryRepository{findErr: ErrNotFound}, keys, verifier, ServiceOptions{
		Now: clock.Now, ChallengeTTL: 5 * time.Minute, RegistrationTTL: ttl, SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Poll(context.Background(), challenge.ID)
	if err != nil || result.RegistrationIntent == "" {
		t.Fatalf("Poll() = %#v, %v", result, err)
	}
	return service, result.RegistrationIntent, keys
}

func fixedServiceKeyring(t *testing.T) security.Keyring {
	t.Helper()
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x31}, 32), bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func nowFunc(now time.Time) func() time.Time {
	return func() time.Time { return now }
}

func assertForgottenExactly(t *testing.T, verifier *memoryVerifier, challengeID string) {
	t.Helper()
	got := verifier.forgotten()
	if len(got) != 1 || got[0] != challengeID {
		t.Fatalf("Forget calls = %v, want [%s]", got, challengeID)
	}
}

func pointerTime(value time.Time) *time.Time { return &value }

func waitForServiceState(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for physical TTL cleanup")
		case <-ticker.C:
		}
	}
}

type mutableServiceClock struct {
	mu    sync.Mutex
	value time.Time
}

func (clock *mutableServiceClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.value
}

func (clock *mutableServiceClock) Set(value time.Time) {
	clock.mu.Lock()
	clock.value = value
	clock.mu.Unlock()
}
