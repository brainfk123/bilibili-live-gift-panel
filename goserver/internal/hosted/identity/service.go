package identity

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"strconv"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/hosted/security"
)

const (
	ChallengePending     = "pending"
	ChallengeScanned     = "scanned"
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
	ID              string    `json:"challengeId"`
	QRImage         string    `json:"qrImage"`
	VerificationURL string    `json:"verificationUrl,omitempty"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

// ValidateBilibiliVerificationURL keeps the public mobile verification link
// on the same narrow Bilibili surface encoded by the QR image.
func ValidateBilibiliVerificationURL(raw string) (canonical, qrKey string, ok bool) {
	if raw == "" || len(raw) > 2048 {
		return "", "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
		return "", "", false
	}
	currentMobilePath := parsed.Host == "account.bilibili.com" && parsed.Path == "/h5/account-h5/auth/scan-web"
	allowedPath := (parsed.Host == "passport.bilibili.com" && parsed.Path == "/h5-app/passport/login/scan") ||
		(parsed.Host == "account.bilibili.com" && parsed.Path == "/scan") || currentMobilePath
	if !allowedPath {
		return "", "", false
	}
	query := parsed.Query()
	for key := range query {
		if key != "qrcode_key" && key != "navhide" && !(currentMobilePath && (key == "callback" || key == "from")) {
			return "", "", false
		}
	}
	keys := query["qrcode_key"]
	if len(keys) != 1 || keys[0] == "" || len(keys[0]) > 512 {
		return "", "", false
	}
	if navhide, present := query["navhide"]; present && (len(navhide) != 1 || navhide[0] != "1") {
		return "", "", false
	}
	if currentMobilePath {
		callback := query["callback"]
		navhide := query["navhide"]
		from := query["from"]
		if len(callback) != 1 || callback[0] != "close" || len(navhide) != 1 || navhide[0] != "1" || (len(from) != 0 && (len(from) != 1 || from[0] != "")) {
			return "", "", false
		}
	}
	return parsed.String(), keys[0], true
}

// Verification is the only identity material allowed out of a Bilibili QR
// adapter. In particular, it cannot carry Cookie or refresh-token data.
type Verification struct {
	UID         string    `json:"uid"`
	CompletedAt time.Time `json:"completedAt"`
}

// VerificationStage is the public progress state reported by a Bilibili QR
// verifier. Only a verified stage may carry identity material.
type VerificationStage string

const (
	VerificationWaiting  VerificationStage = "waiting"
	VerificationScanned  VerificationStage = "scanned"
	VerificationVerified VerificationStage = "verified"
)

// VerificationPoll separates non-terminal QR progress from a completed
// identity proof. Waiting and scanned polls always carry a zero Verification.
type VerificationPoll struct {
	Stage        VerificationStage
	Verification Verification
}

// BiliVerifier proves control of a Bilibili account with ephemeral state.
type BiliVerifier interface {
	Begin(context.Context) (Challenge, error)
	Poll(context.Context, string) (VerificationPoll, error)
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
	Now               func() time.Time
	ChallengeTTL      time.Duration
	RegistrationTTL   time.Duration
	SessionTTL        time.Duration
	OnAccountDisabled func(int64)
}

type challengeStage uint8

const (
	challengePolling challengeStage = iota
	challengeLoginReady
)

type serviceChallenge struct {
	expiresAt         time.Time
	completedAt       time.Time
	stage             challengeStage
	account           Account
	verifierForgotten bool
	pollInProgress    bool
	timer             *time.Timer
}

type registrationIntent struct {
	uid         EncryptedUID
	expiresAt   time.Time
	timer       *time.Timer
	reservation *registrationReservation
}

// RegistrationIntentReservation is a short-lived, single-winner lease over
// one registration intent. Identity returns caller-owned clones. Callers must
// Commit only after their durable transaction commits and Abort on every
// failure; both operations are idempotent.
type RegistrationIntentReservation interface {
	Identity() (EncryptedUID, time.Time, bool)
	Valid() bool
	Commit()
	Abort()
}

type registrationReservation struct {
	service   *Service
	key       [sha256.Size]byte
	intent    *registrationIntent
	uid       EncryptedUID
	expiresAt time.Time
	done      bool
}

// Service owns the short-lived bridge between a Bilibili proof and a hosted
// site session. Its maps are process-local by design.
type Service struct {
	repository        Repository
	keys              security.Keyring
	verifier          BiliVerifier
	now               func() time.Time
	challengeTTL      time.Duration
	registrationTTL   time.Duration
	sessionTTL        time.Duration
	onAccountDisabled func(int64)

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
		onAccountDisabled: options.OnAccountDisabled,
		challenges:        make(map[string]*serviceChallenge), registrations: make(map[[sha256.Size]byte]*registrationIntent),
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
	if challenge.VerificationURL != "" {
		canonical, _, ok := ValidateBilibiliVerificationURL(challenge.VerificationURL)
		if !ok {
			service.verifier.Forget(challenge.ID)
			return Challenge{}, ErrVerificationFailed
		}
		challenge.VerificationURL = canonical
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

	poll, err := service.verifier.Poll(ctx, challengeID)
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
	switch poll.Stage {
	case VerificationWaiting:
		if poll.Verification != (Verification{}) {
			service.removeAndForget(challengeID)
			return PollResult{}, ErrAuthenticationFailed
		}
		service.finishPoll(challengeID)
		return PollResult{Status: ChallengePending, ExpiresAt: state.expiresAt}, nil
	case VerificationScanned:
		if poll.Verification != (Verification{}) {
			service.removeAndForget(challengeID)
			return PollResult{}, ErrAuthenticationFailed
		}
		service.finishPoll(challengeID)
		return PollResult{Status: ChallengeScanned, ExpiresAt: state.expiresAt}, nil
	case VerificationVerified:
	default:
		service.removeAndForget(challengeID)
		return PollResult{}, ErrAuthenticationFailed
	}
	verification := poll.Verification

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
		state.completedAt = verification.CompletedAt.UTC()
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

// ReserveRegistrationIntent leases one verified encrypted UID without
// irreversibly consuming it. Only one reservation can exist for a token.
func (service *Service) ReserveRegistrationIntent(token string) (RegistrationIntentReservation, error) {
	if service == nil || token == "" {
		return nil, ErrRegistrationIntentInvalid
	}
	service.collectExpired()
	hash, err := service.keys.HashToken("registration_intent", []byte(token))
	if err != nil {
		return nil, ErrRegistrationIntentInvalid
	}
	key, ok := digestKey(hash)
	if !ok {
		return nil, ErrRegistrationIntentInvalid
	}
	service.mu.Lock()
	intent, exists := service.registrations[key]
	if !exists || service.closed || intent.reservation != nil || !service.now().Before(intent.expiresAt) {
		if exists && !service.now().Before(intent.expiresAt) {
			delete(service.registrations, key)
			stopRegistrationTimer(intent)
			destroyRegistration(intent)
		}
		service.mu.Unlock()
		return nil, ErrRegistrationIntentInvalid
	}
	reservation := &registrationReservation{
		uid: EncryptedUID{
			Ciphertext: append([]byte(nil), intent.uid.Ciphertext...),
			Lookup:     append([]byte(nil), intent.uid.Lookup...),
		},
		expiresAt: intent.expiresAt,
		service:   service,
		key:       key,
		intent:    intent,
	}
	intent.reservation = reservation
	service.mu.Unlock()
	return reservation, nil
}

// Identity returns a fresh clone of the encrypted UID and the intent's
// absolute expiry while the reservation is live.
func (reservation *registrationReservation) Identity() (EncryptedUID, time.Time, bool) {
	if reservation == nil || reservation.service == nil {
		return EncryptedUID{}, time.Time{}, false
	}
	service := reservation.service
	service.mu.Lock()
	defer service.mu.Unlock()
	intent := service.registrations[reservation.key]
	if reservation.done || service.closed || intent != reservation.intent || intent == nil ||
		intent.reservation != reservation || !service.now().Before(reservation.expiresAt) {
		return EncryptedUID{}, reservation.expiresAt, false
	}
	return EncryptedUID{
		Ciphertext: append([]byte(nil), reservation.uid.Ciphertext...),
		Lookup:     append([]byte(nil), reservation.uid.Lookup...),
	}, reservation.expiresAt, true
}

// Valid reports whether the reservation is still the live lease and is before
// its absolute expiry. Invitation redemption rechecks this immediately before
// committing its SQL transaction.
func (reservation *registrationReservation) Valid() bool {
	if reservation == nil || reservation.service == nil {
		return false
	}
	service := reservation.service
	service.mu.Lock()
	defer service.mu.Unlock()
	intent := service.registrations[reservation.key]
	return !reservation.done && !service.closed && intent == reservation.intent && intent != nil &&
		intent.reservation == reservation && service.now().Before(reservation.expiresAt)
}

// Commit irreversibly destroys the leased intent. It is deliberately
// infallible so it can follow a successful durable SQL commit.
func (reservation *registrationReservation) Commit() {
	if reservation == nil || reservation.service == nil {
		return
	}
	service := reservation.service
	service.mu.Lock()
	if reservation.done {
		service.mu.Unlock()
		return
	}
	reservation.done = true
	if intent := service.registrations[reservation.key]; intent == reservation.intent && intent.reservation == reservation {
		delete(service.registrations, reservation.key)
		stopRegistrationTimer(intent)
		destroyRegistration(intent)
	}
	service.mu.Unlock()
	destroyReservation(reservation)
}

// Abort releases a live lease for retry until its original absolute expiry.
// Expired or closed intents are destroyed instead of restored.
func (reservation *registrationReservation) Abort() {
	if reservation == nil || reservation.service == nil {
		return
	}
	service := reservation.service
	service.mu.Lock()
	if reservation.done {
		service.mu.Unlock()
		return
	}
	reservation.done = true
	if intent := service.registrations[reservation.key]; intent == reservation.intent && intent.reservation == reservation {
		if !service.closed && service.now().Before(intent.expiresAt) {
			intent.reservation = nil
		} else {
			delete(service.registrations, reservation.key)
			stopRegistrationTimer(intent)
			destroyRegistration(intent)
		}
	}
	service.mu.Unlock()
	destroyReservation(reservation)
}

// ConsumeRegistrationIntent is the compatibility wrapper for callers that do
// not need rollback: reserve, clone the identity, then commit irreversibly.
func (service *Service) ConsumeRegistrationIntent(token string) (EncryptedUID, error) {
	reservation, err := service.ReserveRegistrationIntent(token)
	if err != nil {
		return EncryptedUID{}, err
	}
	result, _, ok := reservation.Identity()
	if !ok {
		reservation.Abort()
		return EncryptedUID{}, ErrRegistrationIntentInvalid
	}
	reservation.Commit()
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

// ConsumeAccountProof consumes one verified existing-account challenge for a
// sensitive account-owned operation. It deliberately returns no identity or
// credential material. Pending verification remains pollable; every terminal
// proof outcome removes the challenge so it cannot be replayed.
func (service *Service) ConsumeAccountProof(_ context.Context, challengeID string, accountID int64, maxAge time.Duration) error {
	if service == nil || challengeID == "" {
		return ErrAuthenticationFailed
	}
	service.collectExpired()
	service.mu.Lock()
	state, exists := service.challenges[challengeID]
	if !exists || service.closed {
		service.mu.Unlock()
		return ErrAuthenticationFailed
	}
	if state.stage == challengePolling {
		service.mu.Unlock()
		return ErrVerificationPending
	}
	delete(service.challenges, challengeID)
	stopChallengeTimer(state)
	service.mu.Unlock()

	now := service.now()
	if accountID <= 0 || maxAge <= 0 || state.account.ID <= 0 || state.account.ID != accountID ||
		state.completedAt.IsZero() || state.completedAt.After(now) || now.Sub(state.completedAt) > maxAge {
		return ErrAuthenticationFailed
	}
	return nil
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
	if intent.reservation != nil {
		intent.reservation.done = true
		destroyReservation(intent.reservation)
		intent.reservation = nil
	}
	destroyEncryptedUID(&intent.uid)
	intent.uid = EncryptedUID{}
}

func destroyReservation(reservation *registrationReservation) {
	if reservation == nil {
		return
	}
	destroyEncryptedUID(&reservation.uid)
	reservation.uid = EncryptedUID{}
}

func destroyEncryptedUID(uid *EncryptedUID) {
	if uid == nil {
		return
	}
	for index := range uid.Ciphertext {
		uid.Ciphertext[index] = 0
	}
	for index := range uid.Lookup {
		uid.Lookup[index] = 0
	}
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
