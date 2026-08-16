package identity

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/hosted/security"
)

const (
	ChallengePending     = "pending"
	ChallengeVerified    = "verified"
	RegistrationRequired = "registration_required"
)

var (
	ErrVerificationPending       = errors.New("identity: verification pending")
	ErrVerificationUnavailable   = errors.New("identity: verification unavailable")
	ErrVerificationFailed        = errors.New("identity: verification failed")
	ErrChallengeExpired          = errors.New("identity: challenge expired")
	ErrChallengeNotFound         = errors.New("identity: challenge not found")
	ErrAuthenticationFailed      = errors.New("identity: authentication failed")
	ErrRegistrationIntentInvalid = errors.New("identity: registration intent invalid")
	ErrServiceClosed             = errors.New("identity: service closed")
)

// Challenge is the public, credential-free representation of one Bilibili QR
// verification attempt. QRImage is intended for immediate display only.
type Challenge struct {
	ID        string    `json:"challengeId"`
	QRImage   string    `json:"qrImage"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Verification is the only identity material allowed out of a Bilibili QR
// adapter. In particular, it cannot carry Cookie or refresh-token data.
type Verification struct {
	UID         string    `json:"uid"`
	CompletedAt time.Time `json:"completedAt"`
}

// BiliVerifier proves control of a Bilibili account with ephemeral state.
type BiliVerifier interface {
	Begin(context.Context) (Challenge, error)
	Poll(context.Context, string) (Verification, error)
	Forget(string)
}

// PollResult deliberately reveals neither UID nor an internal account ID.
type PollResult struct {
	Status             string    `json:"status"`
	RegistrationIntent string    `json:"registrationIntent,omitempty"`
	ExpiresAt          time.Time `json:"expiresAt,omitempty"`
}

// SiteSession contains the plaintext site token only during the call that sets
// the browser cookie. Token is excluded from accidental JSON serialization.
type SiteSession struct {
	Token     string    `json:"-"`
	AccountID int64     `json:"-"`
	ExpiresAt time.Time `json:"-"`
}

// ServiceOptions contains bounded lifetimes and an injectable clock.
type ServiceOptions struct {
	Now             func() time.Time
	ChallengeTTL    time.Duration
	RegistrationTTL time.Duration
	SessionTTL      time.Duration
}

type challengeStage uint8

const (
	challengePolling challengeStage = iota
	challengeLoginReady
)

type serviceChallenge struct {
	expiresAt         time.Time
	stage             challengeStage
	account           Account
	verifierForgotten bool
	pollInProgress    bool
	timer             *time.Timer
}

type registrationIntent struct {
	uid       EncryptedUID
	expiresAt time.Time
	timer     *time.Timer
}

// Service owns the short-lived bridge between a Bilibili proof and a hosted
// site session. Its maps are process-local by design.
type Service struct {
	repository      Repository
	keys            security.Keyring
	verifier        BiliVerifier
	now             func() time.Time
	challengeTTL    time.Duration
	registrationTTL time.Duration
	sessionTTL      time.Duration

	mu            sync.Mutex
	closed        bool
	challenges    map[string]*serviceChallenge
	registrations map[[sha256.Size]byte]*registrationIntent
}

// NewService constructs the identity service without starting background
// workers. Expired process-local state is collected on every service call.
func NewService(repository Repository, keys security.Keyring, verifier BiliVerifier, options ServiceOptions) (*Service, error) {
	if repository == nil || verifier == nil {
		return nil, ErrInvalidInput
	}
	if _, err := keys.HashToken("site_session", []byte("constructor-check")); err != nil {
		return nil, ErrInvalidInput
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ChallengeTTL == 0 {
		options.ChallengeTTL = 5 * time.Minute
	}
	if options.RegistrationTTL == 0 {
		options.RegistrationTTL = 5 * time.Minute
	}
	if options.SessionTTL == 0 {
		options.SessionTTL = 24 * time.Hour
	}
	if options.ChallengeTTL <= 0 || options.ChallengeTTL > 5*time.Minute || options.RegistrationTTL <= 0 || options.RegistrationTTL > 5*time.Minute || options.SessionTTL <= 0 {
		return nil, ErrInvalidInput
	}
	return &Service{
		repository: repository, keys: keys, verifier: verifier, now: options.Now,
		challengeTTL: options.ChallengeTTL, registrationTTL: options.RegistrationTTL, sessionTTL: options.SessionTTL,
		challenges: make(map[string]*serviceChallenge), registrations: make(map[[sha256.Size]byte]*registrationIntent),
	}, nil
}

// Begin starts a bounded QR challenge and records no Bilibili credential in the
// service layer.
func (service *Service) Begin(ctx context.Context) (Challenge, error) {
	if service == nil {
		return Challenge{}, ErrServiceClosed
	}
	service.collectExpired()
	challenge, err := service.verifier.Begin(ctx)
	if err != nil {
		return Challenge{}, ErrVerificationUnavailable
	}
	now := service.now()
	if challenge.ID == "" || len(challenge.ID) > 256 || challenge.QRImage == "" || len(challenge.QRImage) > 2<<20 || !challenge.ExpiresAt.After(now) {
		service.verifier.Forget(challenge.ID)
		return Challenge{}, ErrVerificationFailed
	}
	maximumExpiry := now.Add(service.challengeTTL)
	if challenge.ExpiresAt.After(maximumExpiry) {
		challenge.ExpiresAt = maximumExpiry
	}

	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		service.verifier.Forget(challenge.ID)
		return Challenge{}, ErrServiceClosed
	}
	if _, exists := service.challenges[challenge.ID]; exists {
		service.mu.Unlock()
		service.verifier.Forget(challenge.ID)
		return Challenge{}, ErrVerificationFailed
	}
	state := &serviceChallenge{expiresAt: challenge.ExpiresAt, stage: challengePolling}
	service.challenges[challenge.ID] = state
	state.timer = time.AfterFunc(challenge.ExpiresAt.Sub(now), func() {
		service.expireChallenge(challenge.ID, state)
	})
	service.mu.Unlock()
	return challenge, nil
}

// Poll advances a QR challenge. Terminal verifier results are forgotten before
// this method returns, including all failures after Bilibili confirmation.
func (service *Service) Poll(ctx context.Context, challengeID string) (PollResult, error) {
	if service == nil || challengeID == "" {
		return PollResult{}, ErrAuthenticationFailed
	}
	service.collectExpired()
	service.mu.Lock()
	state, exists := service.challenges[challengeID]
	if !exists || service.closed || state.stage != challengePolling {
		service.mu.Unlock()
		return PollResult{}, ErrAuthenticationFailed
	}
	if state.pollInProgress {
		service.mu.Unlock()
		return PollResult{Status: ChallengePending, ExpiresAt: state.expiresAt}, nil
	}
	state.pollInProgress = true
	service.mu.Unlock()

	verification, err := service.verifier.Poll(ctx, challengeID)
	if errors.Is(err, ErrVerificationPending) {
		service.finishPoll(challengeID)
		return PollResult{Status: ChallengePending, ExpiresAt: state.expiresAt}, nil
	}
	if errors.Is(err, ErrVerificationUnavailable) {
		service.finishPoll(challengeID)
		return PollResult{}, ErrVerificationUnavailable
	}
	if err != nil {
		service.removeAndForget(challengeID)
		if errors.Is(err, ErrChallengeExpired) {
			return PollResult{}, ErrChallengeExpired
		}
		return PollResult{}, ErrAuthenticationFailed
	}

	uid, valid := canonicalUID(verification.UID)
	now := service.now()
	if !valid || verification.CompletedAt.IsZero() || verification.CompletedAt.After(now.Add(time.Minute)) || verification.CompletedAt.Before(now.Add(-service.challengeTTL)) {
		service.removeAndForget(challengeID)
		return PollResult{}, ErrAuthenticationFailed
	}
	lookup, err := service.keys.Lookup("bili_uid", []byte(uid))
	if err != nil {
		service.removeAndForget(challengeID)
		return PollResult{}, ErrAuthenticationFailed
	}
	account, err := service.repository.FindAccountByUIDLookup(ctx, lookup)
	if err == nil {
		if account.ID <= 0 || account.CredentialEpoch < 1 || account.DisabledAt != nil {
			service.removeAndForget(challengeID)
			return PollResult{}, ErrAuthenticationFailed
		}
		service.mu.Lock()
		current, present := service.challenges[challengeID]
		if !present || current != state || service.closed {
			service.mu.Unlock()
			service.removeAndForget(challengeID)
			return PollResult{}, ErrAuthenticationFailed
		}
		if !service.now().Before(state.expiresAt) {
			delete(service.challenges, challengeID)
			stopChallengeTimer(state)
			state.verifierForgotten = true
			service.mu.Unlock()
			service.verifier.Forget(challengeID)
			return PollResult{}, ErrChallengeExpired
		}
		state.stage = challengeLoginReady
		state.account = account
		state.pollInProgress = false
		state.verifierForgotten = true
		service.mu.Unlock()
		service.verifier.Forget(challengeID)
		return PollResult{Status: ChallengeVerified, ExpiresAt: state.expiresAt}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		service.removeAndForget(challengeID)
		return PollResult{}, ErrAuthenticationFailed
	}

	sealed, err := service.keys.Seal("bili_uid", []byte(uid))
	if err != nil {
		service.removeAndForget(challengeID)
		return PollResult{}, ErrAuthenticationFailed
	}
	intentToken, err := service.keys.NewToken()
	if err != nil {
		service.removeAndForget(challengeID)
		return PollResult{}, ErrAuthenticationFailed
	}
	intentHash, err := service.keys.HashToken("registration_intent", []byte(intentToken))
	if err != nil {
		service.removeAndForget(challengeID)
		return PollResult{}, ErrAuthenticationFailed
	}
	intentKey, ok := digestKey(intentHash)
	if !ok {
		service.removeAndForget(challengeID)
		return PollResult{}, ErrAuthenticationFailed
	}
	service.mu.Lock()
	current, present := service.challenges[challengeID]
	if !present || current != state || service.closed {
		service.mu.Unlock()
		return PollResult{}, ErrAuthenticationFailed
	}
	commitTime := service.now()
	if !commitTime.Before(state.expiresAt) {
		delete(service.challenges, challengeID)
		stopChallengeTimer(state)
		state.verifierForgotten = true
		service.mu.Unlock()
		service.verifier.Forget(challengeID)
		return PollResult{}, ErrChallengeExpired
	}
	if _, collision := service.registrations[intentKey]; collision {
		delete(service.challenges, challengeID)
		stopChallengeTimer(state)
		state.verifierForgotten = true
		service.mu.Unlock()
		service.verifier.Forget(challengeID)
		return PollResult{}, ErrAuthenticationFailed
	}
	delete(service.challenges, challengeID)
	stopChallengeTimer(state)
	state.verifierForgotten = true
	expiresAt := commitTime.Add(service.registrationTTL)
	intent := &registrationIntent{
		uid:       EncryptedUID{Ciphertext: sealed, Lookup: append([]byte(nil), lookup...)},
		expiresAt: expiresAt,
	}
	service.registrations[intentKey] = intent
	intent.timer = time.AfterFunc(service.registrationTTL, func() {
		service.expireRegistration(intentKey, intent)
	})
	service.mu.Unlock()
	service.verifier.Forget(challengeID)
	return PollResult{Status: RegistrationRequired, RegistrationIntent: intentToken, ExpiresAt: expiresAt}, nil
}

// ConsumeRegistrationIntent atomically returns and destroys one verified,
// encrypted UID. It is the narrow seam consumed by invitation redemption.
func (service *Service) ConsumeRegistrationIntent(token string) (EncryptedUID, error) {
	if service == nil || token == "" {
		return EncryptedUID{}, ErrRegistrationIntentInvalid
	}
	service.collectExpired()
	hash, err := service.keys.HashToken("registration_intent", []byte(token))
	if err != nil {
		return EncryptedUID{}, ErrRegistrationIntentInvalid
	}
	key, ok := digestKey(hash)
	if !ok {
		return EncryptedUID{}, ErrRegistrationIntentInvalid
	}
	service.mu.Lock()
	intent, exists := service.registrations[key]
	if exists {
		delete(service.registrations, key)
		stopRegistrationTimer(intent)
	}
	closed := service.closed
	service.mu.Unlock()
	if !exists || closed || !service.now().Before(intent.expiresAt) {
		if exists {
			destroyRegistration(intent)
		}
		return EncryptedUID{}, ErrRegistrationIntentInvalid
	}
	result := EncryptedUID{Ciphertext: append([]byte(nil), intent.uid.Ciphertext...), Lookup: append([]byte(nil), intent.uid.Lookup...)}
	destroyRegistration(intent)
	return result, nil
}

// Login consumes a verified existing-account challenge and creates a hash-only
// repository session.
func (service *Service) Login(ctx context.Context, challengeID string) (SiteSession, error) {
	if service == nil || challengeID == "" {
		return SiteSession{}, ErrAuthenticationFailed
	}
	service.collectExpired()
	service.mu.Lock()
	state, exists := service.challenges[challengeID]
	if exists {
		delete(service.challenges, challengeID)
		stopChallengeTimer(state)
	}
	closed := service.closed
	service.mu.Unlock()
	if !exists || closed || state.stage != challengeLoginReady || !service.now().Before(state.expiresAt) {
		return SiteSession{}, ErrAuthenticationFailed
	}
	token, err := service.keys.NewToken()
	if err != nil {
		return SiteSession{}, ErrAuthenticationFailed
	}
	hash, err := service.keys.HashToken("site_session", []byte(token))
	if err != nil {
		return SiteSession{}, ErrAuthenticationFailed
	}
	now := service.now()
	expiresAt := now.Add(service.sessionTTL)
	err = service.repository.CreateSession(ctx, Session{
		AccountID: state.account.ID, TokenHash: hash, CredentialEpoch: state.account.CredentialEpoch,
		CreatedAt: now, ExpiresAt: expiresAt,
	})
	if err != nil {
		return SiteSession{}, ErrAuthenticationFailed
	}
	return SiteSession{Token: token, AccountID: state.account.ID, ExpiresAt: expiresAt}, nil
}

// RequireSession hashes the caller's Cookie value before crossing the
// repository boundary.
func (service *Service) RequireSession(ctx context.Context, token string) (Session, error) {
	if service == nil || token == "" {
		return Session{}, ErrAuthenticationFailed
	}
	hash, err := service.keys.HashToken("site_session", []byte(token))
	if err != nil {
		return Session{}, ErrAuthenticationFailed
	}
	session, err := service.repository.FindSessionByHash(ctx, hash, service.now())
	if err != nil {
		return Session{}, ErrAuthenticationFailed
	}
	return session, nil
}

// Logout revokes a site session by hash and is safe to retry.
func (service *Service) Logout(ctx context.Context, token string) error {
	if service == nil || token == "" {
		return ErrAuthenticationFailed
	}
	hash, err := service.keys.HashToken("site_session", []byte(token))
	if err != nil {
		return ErrAuthenticationFailed
	}
	if err := service.repository.RevokeSession(ctx, hash); err != nil {
		return ErrAuthenticationFailed
	}
	return nil
}

// Cancel destroys any nonterminal verifier state for challengeID.
func (service *Service) Cancel(challengeID string) {
	if service == nil || challengeID == "" {
		return
	}
	service.removeAndForget(challengeID)
}

// Close destroys every outstanding challenge and registration intent. It is
// idempotent and intended to be called from process shutdown wiring.
func (service *Service) Close() {
	if service == nil {
		return
	}
	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		return
	}
	service.closed = true
	forget := make([]string, 0, len(service.challenges))
	for challengeID, state := range service.challenges {
		stopChallengeTimer(state)
		if !state.verifierForgotten {
			forget = append(forget, challengeID)
		}
	}
	for _, intent := range service.registrations {
		stopRegistrationTimer(intent)
		destroyRegistration(intent)
	}
	clear(service.challenges)
	clear(service.registrations)
	service.mu.Unlock()
	for _, challengeID := range forget {
		service.verifier.Forget(challengeID)
	}
}

func (service *Service) finishPoll(challengeID string) {
	service.mu.Lock()
	if state := service.challenges[challengeID]; state != nil {
		state.pollInProgress = false
	}
	service.mu.Unlock()
}

func (service *Service) removeAndForget(challengeID string) {
	service.mu.Lock()
	state, exists := service.challenges[challengeID]
	if exists {
		delete(service.challenges, challengeID)
		stopChallengeTimer(state)
	}
	service.mu.Unlock()
	if exists && !state.verifierForgotten {
		service.verifier.Forget(challengeID)
	}
}

func (service *Service) collectExpired() {
	now := service.now()
	service.mu.Lock()
	forget := make([]string, 0)
	for challengeID, state := range service.challenges {
		if !now.Before(state.expiresAt) {
			delete(service.challenges, challengeID)
			stopChallengeTimer(state)
			if !state.verifierForgotten {
				forget = append(forget, challengeID)
			}
		}
	}
	for key, intent := range service.registrations {
		if !now.Before(intent.expiresAt) {
			delete(service.registrations, key)
			stopRegistrationTimer(intent)
			destroyRegistration(intent)
		}
	}
	service.mu.Unlock()
	for _, challengeID := range forget {
		service.verifier.Forget(challengeID)
	}
}

func (service *Service) expireChallenge(challengeID string, expected *serviceChallenge) {
	service.mu.Lock()
	state, exists := service.challenges[challengeID]
	if !exists || state != expected {
		service.mu.Unlock()
		return
	}
	delete(service.challenges, challengeID)
	state.timer = nil
	shouldForget := !state.verifierForgotten
	service.mu.Unlock()
	if shouldForget {
		service.verifier.Forget(challengeID)
	}
}

func (service *Service) expireRegistration(key [sha256.Size]byte, expected *registrationIntent) {
	service.mu.Lock()
	intent, exists := service.registrations[key]
	if !exists || intent != expected {
		service.mu.Unlock()
		return
	}
	delete(service.registrations, key)
	intent.timer = nil
	destroyRegistration(intent)
	service.mu.Unlock()
}

func stopChallengeTimer(state *serviceChallenge) {
	if state != nil && state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
}

func stopRegistrationTimer(intent *registrationIntent) {
	if intent != nil && intent.timer != nil {
		intent.timer.Stop()
		intent.timer = nil
	}
}

func destroyRegistration(intent *registrationIntent) {
	if intent == nil {
		return
	}
	for index := range intent.uid.Ciphertext {
		intent.uid.Ciphertext[index] = 0
	}
	for index := range intent.uid.Lookup {
		intent.uid.Lookup[index] = 0
	}
	intent.uid = EncryptedUID{}
}

func canonicalUID(value string) (string, bool) {
	if value == "" || len(value) > 20 {
		return "", false
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return "", false
	}
	canonical := strconv.FormatUint(parsed, 10)
	return canonical, canonical == value
}

func digestKey(value []byte) ([sha256.Size]byte, bool) {
	var key [sha256.Size]byte
	if len(value) != len(key) {
		return key, false
	}
	copy(key[:], value)
	return key, true
}
