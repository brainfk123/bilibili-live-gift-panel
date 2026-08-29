// Package biliqr implements the true-external Bilibili QR verification port.
// It deliberately has no persistence dependency.
package biliqr

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	defaultGenerateEndpoint = "https://passport.bilibili.com/x/passport-login/web/qrcode/generate"
	defaultPollEndpoint     = "https://passport.bilibili.com/x/passport-login/web/qrcode/poll"
	defaultNavEndpoint      = "https://api.bilibili.com/x/web-interface/nav"
	maximumLifetime         = 5 * time.Minute
	defaultPollInterval     = 2 * time.Second
	maximumResponseBytes    = 1 << 20
)

// Config supports deterministic external-contract tests without weakening
// production endpoint defaults.
type Config struct {
	Client           *http.Client
	GenerateEndpoint string
	PollEndpoint     string
	NavEndpoint      string
	Now              func() time.Time
	Lifetime         time.Duration
	PollInterval     time.Duration
	EncodeQR         func(string) (string, error)
	Diagnostic       func(DiagnosticEvent)
}

// DiagnosticEvent reports only a fixed reason and numeric upstream states.
// It must never contain a challenge ID, QR key, URL, Cookie, UID, or account ID.
type DiagnosticEvent struct {
	Reason       string
	UpstreamCode int
	StageCode    int
}

type pendingChallenge struct {
	qrKey      string
	expiresAt  time.Time
	cookies    map[string]string
	terminal   error
	consuming  bool
	polling    bool
	nextPollAt time.Time
	timer      *time.Timer
	cancel     context.CancelCauseFunc
}

// CredentialConsumer is an internal trusted boundary for the one operation
// that needs a Bilibili session Cookie. Its context is canceled at the
// challenge's absolute TTL and when the adapter forgets or closes the
// challenge. The byte slice is invalid once ConsumeCredential returns.
type CredentialConsumer = func(context.Context, []byte) error

// Adapter keeps QR keys and confirmation Cookies only in process memory.
type Adapter struct {
	client           *http.Client
	generateEndpoint string
	pollEndpoint     string
	navEndpoint      string
	now              func() time.Time
	lifetime         time.Duration
	pollInterval     time.Duration
	encodeQR         func(string) (string, error)
	diagnostic       func(DiagnosticEvent)

	mu         sync.Mutex
	closed     bool
	challenges map[string]*pendingChallenge
}

// New constructs a Bilibili QR adapter with a hard five-minute maximum TTL.
func New(config Config) (*Adapter, error) {
	if config.Client == nil {
		config.Client = &http.Client{Timeout: 8 * time.Second}
	}
	if config.GenerateEndpoint == "" {
		config.GenerateEndpoint = defaultGenerateEndpoint
	}
	if config.PollEndpoint == "" {
		config.PollEndpoint = defaultPollEndpoint
	}
	if config.NavEndpoint == "" {
		config.NavEndpoint = defaultNavEndpoint
	}
	for _, endpoint := range []string{config.GenerateEndpoint, config.PollEndpoint, config.NavEndpoint} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, identity.ErrVerificationFailed
		}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Lifetime == 0 {
		config.Lifetime = maximumLifetime
	}
	if config.Lifetime <= 0 {
		return nil, identity.ErrVerificationFailed
	}
	if config.Lifetime > maximumLifetime {
		config.Lifetime = maximumLifetime
	}
	if config.PollInterval == 0 {
		config.PollInterval = defaultPollInterval
	}
	if config.PollInterval < 0 || config.PollInterval > 30*time.Second {
		return nil, identity.ErrVerificationFailed
	}
	if config.EncodeQR == nil {
		config.EncodeQR = encodeQRImage
	}
	return &Adapter{
		client: config.Client, generateEndpoint: config.GenerateEndpoint, pollEndpoint: config.PollEndpoint, navEndpoint: config.NavEndpoint,
		now: config.Now, lifetime: config.Lifetime, pollInterval: config.PollInterval, encodeQR: config.EncodeQR, diagnostic: config.Diagnostic,
		challenges: make(map[string]*pendingChallenge),
	}, nil
}

// Begin creates a Bilibili QR code and replaces its upstream key with an
// independent random hosted challenge ID.
func (adapter *Adapter) Begin(ctx context.Context) (identity.Challenge, error) {
	var payload struct {
		Code int `json:"code"`
		Data struct {
			URL string `json:"url"`
			Key string `json:"qrcode_key"`
		} `json:"data"`
	}
	if _, err := adapter.getJSON(ctx, adapter.generateEndpoint, nil, &payload); err != nil {
		return identity.Challenge{}, err
	}
	if payload.Code != 0 || payload.Data.URL == "" || payload.Data.Key == "" {
		return identity.Challenge{}, identity.ErrVerificationFailed
	}
	verificationURL, qrKey, ok := identity.ValidateBilibiliVerificationURL(payload.Data.URL)
	if !ok || qrKey != payload.Data.Key {
		return identity.Challenge{}, identity.ErrVerificationFailed
	}
	image, err := adapter.encodeQR(verificationURL)
	if err != nil || image == "" {
		return identity.Challenge{}, identity.ErrVerificationFailed
	}
	challengeID, err := randomChallengeID()
	if err != nil {
		return identity.Challenge{}, identity.ErrVerificationUnavailable
	}
	expiresAt := adapter.now().Add(adapter.lifetime)
	adapter.mu.Lock()
	if adapter.closed {
		adapter.mu.Unlock()
		return identity.Challenge{}, identity.ErrVerificationUnavailable
	}
	state := &pendingChallenge{qrKey: payload.Data.Key, expiresAt: expiresAt}
	adapter.challenges[challengeID] = state
	state.timer = time.AfterFunc(adapter.lifetime, func() {
		adapter.expireChallenge(challengeID, state)
	})
	adapter.mu.Unlock()
	return identity.Challenge{ID: challengeID, QRImage: image, VerificationURL: verificationURL, ExpiresAt: expiresAt}, nil
}

// Poll returns QR progress and, only after verification, a UID and completion
// time. All Cookies are destroyed on every terminal result for this normal
// identity-verification path.
func (adapter *Adapter) Poll(ctx context.Context, challengeID string) (identity.VerificationPoll, error) {
	return adapter.poll(ctx, challengeID, false)
}

// PollCredential returns QR progress for the service-account path. On a
// verified stage it retains the Cookie inside the adapter until exactly one
// ConsumeCredential call succeeds; it never performs an identity lookup.
func (adapter *Adapter) PollCredential(ctx context.Context, challengeID string) (identity.VerificationStage, error) {
	poll, err := adapter.poll(ctx, challengeID, true)
	return poll.Stage, err
}

func (adapter *Adapter) poll(ctx context.Context, challengeID string, retainCredential bool) (identity.VerificationPoll, error) {
	adapter.mu.Lock()
	state, exists := adapter.challenges[challengeID]
	if !exists || adapter.closed {
		adapter.mu.Unlock()
		return identity.VerificationPoll{}, identity.ErrChallengeNotFound
	}
	if !adapter.now().Before(state.expiresAt) {
		adapter.destroyLocked(challengeID, identity.ErrChallengeExpired)
		adapter.mu.Unlock()
		return identity.VerificationPoll{}, identity.ErrChallengeExpired
	}
	if retainCredential && len(state.cookies) > 0 {
		adapter.mu.Unlock()
		return identity.VerificationPoll{Stage: identity.VerificationVerified}, nil
	}
	if state.consuming {
		adapter.mu.Unlock()
		return identity.VerificationPoll{Stage: identity.VerificationWaiting}, nil
	}
	if state.polling {
		adapter.mu.Unlock()
		return identity.VerificationPoll{Stage: identity.VerificationWaiting}, nil
	}
	now := adapter.now()
	if !state.nextPollAt.IsZero() && now.Before(state.nextPollAt) {
		adapter.mu.Unlock()
		return identity.VerificationPoll{Stage: identity.VerificationWaiting}, nil
	}
	state.polling = true
	state.nextPollAt = now.Add(adapter.pollInterval)
	qrKey := state.qrKey
	pollContext, cancelPoll := context.WithCancelCause(ctx)
	state.cancel = cancelPoll
	adapter.mu.Unlock()
	defer cancelPoll(nil)

	endpoint, err := url.Parse(adapter.pollEndpoint)
	if err != nil {
		adapter.finishTransientPoll(challengeID, state)
		return identity.VerificationPoll{}, identity.ErrVerificationUnavailable
	}
	query := endpoint.Query()
	query.Set("qrcode_key", qrKey)
	endpoint.RawQuery = query.Encode()
	var payload struct {
		Code int `json:"code"`
		Data struct {
			URL  string `json:"url"`
			Code int    `json:"code"`
		} `json:"data"`
	}
	response, err := adapter.getJSON(pollContext, endpoint.String(), nil, &payload)
	if err != nil {
		if terminal := cancellationResult(pollContext); terminal != nil {
			return identity.VerificationPoll{}, terminal
		}
		adapter.finishTransientPoll(challengeID, state)
		return identity.VerificationPoll{}, err
	}
	if payload.Code != 0 {
		adapter.reportDiagnostic(DiagnosticEvent{Reason: "poll_envelope_rejected", UpstreamCode: payload.Code, StageCode: payload.Data.Code})
		adapter.Forget(challengeID)
		return identity.VerificationPoll{}, identity.ErrVerificationFailed
	}
	switch payload.Data.Code {
	case 86101:
		adapter.finishTransientPoll(challengeID, state)
		return identity.VerificationPoll{Stage: identity.VerificationWaiting}, nil
	case 86090:
		adapter.finishTransientPoll(challengeID, state)
		return identity.VerificationPoll{Stage: identity.VerificationScanned}, nil
	case 86038:
		adapter.Forget(challengeID)
		return identity.VerificationPoll{}, identity.ErrChallengeExpired
	case 0:
		cookies, ok := loginCookies(response, payload.Data.URL)
		if !ok {
			adapter.reportDiagnostic(DiagnosticEvent{Reason: "verified_session_missing", UpstreamCode: payload.Code, StageCode: payload.Data.Code})
			adapter.Forget(challengeID)
			return identity.VerificationPoll{}, identity.ErrVerificationFailed
		}
		defer destroyCookies(cookies)
		if !adapter.storeCookies(challengeID, state, cookies) {
			if terminal := cancellationResult(pollContext); terminal != nil {
				return identity.VerificationPoll{}, terminal
			}
			return identity.VerificationPoll{}, identity.ErrChallengeNotFound
		}
		if retainCredential {
			adapter.finishTransientPoll(challengeID, state)
			adapter.mu.Lock()
			current, present := adapter.challenges[challengeID]
			expired := present && current == state && !adapter.now().Before(state.expiresAt)
			if expired {
				adapter.destroyLocked(challengeID, identity.ErrChallengeExpired)
			}
			active := present && current == state && !adapter.closed && !expired
			adapter.mu.Unlock()
			if expired {
				return identity.VerificationPoll{}, identity.ErrChallengeExpired
			}
			if !active {
				return identity.VerificationPoll{}, identity.ErrChallengeNotFound
			}
			return identity.VerificationPoll{Stage: identity.VerificationVerified}, nil
		}
		defer adapter.Forget(challengeID)
		uid, err := adapter.fetchUID(pollContext, cookies)
		if err != nil {
			if terminal := cancellationResult(pollContext); terminal != nil {
				return identity.VerificationPoll{}, terminal
			}
			return identity.VerificationPoll{}, err
		}
		adapter.mu.Lock()
		current, present := adapter.challenges[challengeID]
		expired := present && current == state && !adapter.now().Before(state.expiresAt)
		if expired {
			adapter.destroyLocked(challengeID, identity.ErrChallengeExpired)
		}
		active := present && current == state && !adapter.closed && !expired
		adapter.mu.Unlock()
		if expired {
			return identity.VerificationPoll{}, identity.ErrChallengeExpired
		}
		if !active {
			return identity.VerificationPoll{}, identity.ErrChallengeNotFound
		}
		return identity.VerificationPoll{Stage: identity.VerificationVerified, Verification: identity.Verification{UID: uid, CompletedAt: adapter.now()}}, nil
	default:
		adapter.reportDiagnostic(DiagnosticEvent{Reason: "poll_stage_rejected", UpstreamCode: payload.Code, StageCode: payload.Data.Code})
		adapter.Forget(challengeID)
		return identity.VerificationPoll{}, identity.ErrVerificationFailed
	}
}

func (adapter *Adapter) reportDiagnostic(event DiagnosticEvent) {
	if adapter != nil && adapter.diagnostic != nil {
		adapter.diagnostic(event)
	}
}

// ConsumeCredential completes a service-account QR challenge without ever
// exposing its Cookie through the normal identity.Verification result. A
// failed consumer leaves the completed challenge available until its TTL so a
// transactional persistence failure can be retried; a successful consumer
// destroys every adapter reference before returning.
func (adapter *Adapter) ConsumeCredential(ctx context.Context, challengeID string, consumer CredentialConsumer) error {
	if consumer == nil {
		return identity.ErrVerificationFailed
	}
	stage, err := adapter.PollCredential(ctx, challengeID)
	if err != nil {
		return err
	}
	if stage != identity.VerificationVerified {
		return identity.ErrVerificationPending
	}
	return adapter.consumeCredential(ctx, challengeID, consumer)
}

func (adapter *Adapter) consumeCredential(ctx context.Context, challengeID string, consumer CredentialConsumer) error {
	adapter.mu.Lock()
	state, exists := adapter.challenges[challengeID]
	if !exists || adapter.closed {
		adapter.mu.Unlock()
		return identity.ErrChallengeNotFound
	}
	if !adapter.now().Before(state.expiresAt) {
		adapter.destroyLocked(challengeID, identity.ErrChallengeExpired)
		adapter.mu.Unlock()
		return identity.ErrChallengeExpired
	}
	if state.consuming {
		adapter.mu.Unlock()
		return identity.ErrVerificationPending
	}
	if len(state.cookies) == 0 {
		adapter.mu.Unlock()
		return identity.ErrVerificationFailed
	}
	consumerContext, cleanup := reserveCredentialConsumer(ctx, state)
	credential := []byte(cookieHeader(cloneCookies(state.cookies)))
	adapter.mu.Unlock()
	return adapter.consumeStoredCredential(challengeID, state, consumerContext, cleanup, credential, consumer)
}

func reserveCredentialConsumer(ctx context.Context, state *pendingChallenge) (context.Context, func()) {
	deadlineContext, cancelDeadline := context.WithDeadlineCause(ctx, state.expiresAt, identity.ErrChallengeExpired)
	consumerContext, cancelConsumer := context.WithCancelCause(deadlineContext)
	state.consuming = true
	state.cancel = cancelConsumer
	return consumerContext, func() {
		cancelConsumer(nil)
		cancelDeadline()
	}
}

func (adapter *Adapter) consumeStoredCredential(challengeID string, expected *pendingChallenge, consumerContext context.Context, cleanup func(), credential []byte, consumer CredentialConsumer) error {
	defer clear(credential)
	defer cleanup()
	err := consumer(consumerContext, credential)
	consumerCause := context.Cause(consumerContext)
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	state := adapter.challenges[challengeID]
	if state != expected || adapter.closed {
		if expected.terminal != nil {
			return expected.terminal
		}
		return identity.ErrChallengeNotFound
	}
	if errors.Is(consumerCause, identity.ErrChallengeExpired) {
		adapter.destroyLocked(challengeID, identity.ErrChallengeExpired)
		return identity.ErrChallengeExpired
	}
	if !adapter.now().Before(state.expiresAt) {
		adapter.destroyLocked(challengeID, identity.ErrChallengeExpired)
		return identity.ErrChallengeExpired
	}
	state.cancel = nil
	state.consuming = false
	if err != nil {
		return err
	}
	if consumerCause != nil {
		return consumerCause
	}
	adapter.destroyLocked(challengeID, identity.ErrChallengeNotFound)
	return nil
}

// Forget securely drops the adapter's only references to a QR key and Cookies.
func (adapter *Adapter) Forget(challengeID string) {
	if adapter == nil {
		return
	}
	adapter.mu.Lock()
	adapter.destroyLocked(challengeID, identity.ErrChallengeNotFound)
	adapter.mu.Unlock()
}

// Close destroys all outstanding ephemeral credentials and prevents reuse.
func (adapter *Adapter) Close() error {
	if adapter == nil {
		return nil
	}
	adapter.mu.Lock()
	adapter.closed = true
	for challengeID := range adapter.challenges {
		adapter.destroyLocked(challengeID, identity.ErrChallengeNotFound)
	}
	adapter.mu.Unlock()
	return nil
}

func (adapter *Adapter) fetchUID(ctx context.Context, cookies map[string]string) (string, error) {
	var payload struct {
		Code int `json:"code"`
		Data struct {
			IsLogin bool  `json:"isLogin"`
			MID     int64 `json:"mid"`
		} `json:"data"`
	}
	if _, err := adapter.getJSON(ctx, adapter.navEndpoint, cookies, &payload); err != nil {
		return "", err
	}
	if payload.Code != 0 || !payload.Data.IsLogin || payload.Data.MID <= 0 {
		adapter.reportDiagnostic(DiagnosticEvent{Reason: "identity_lookup_rejected", UpstreamCode: payload.Code, StageCode: 0})
		return "", identity.ErrVerificationFailed
	}
	return strconv.FormatInt(payload.Data.MID, 10), nil
}

func (adapter *Adapter) getJSON(ctx context.Context, endpoint string, cookies map[string]string, target any) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, identity.ErrVerificationUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "gift-panel-hosted/1")
	request.Header.Set("Referer", "https://www.bilibili.com/")
	if len(cookies) > 0 {
		request.Header.Set("Cookie", cookieHeader(cookies))
	}
	response, err := adapter.client.Do(request)
	if err != nil {
		return nil, identity.ErrVerificationUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return response, identity.ErrVerificationUnavailable
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maximumResponseBytes)).Decode(target); err != nil {
		return response, identity.ErrVerificationUnavailable
	}
	return response, nil
}

func (adapter *Adapter) storeCookies(challengeID string, expected *pendingChallenge, cookies map[string]string) bool {
	adapter.mu.Lock()
	stored := false
	if state := adapter.challenges[challengeID]; state == expected && !adapter.closed {
		state.cookies = cloneCookies(cookies)
		stored = true
	}
	adapter.mu.Unlock()
	return stored
}

func (adapter *Adapter) finishTransientPoll(challengeID string, expected *pendingChallenge) {
	adapter.mu.Lock()
	if state := adapter.challenges[challengeID]; state == expected {
		state.polling = false
		state.cancel = nil
	}
	adapter.mu.Unlock()
}

func (adapter *Adapter) destroyLocked(challengeID string, cause error) {
	state := adapter.challenges[challengeID]
	if state == nil {
		return
	}
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
	if state.cancel != nil {
		state.cancel(cause)
		state.cancel = nil
	}
	state.terminal = cause
	state.qrKey = ""
	destroyCookies(state.cookies)
	delete(adapter.challenges, challengeID)
}

func (adapter *Adapter) expireChallenge(challengeID string, expected *pendingChallenge) {
	adapter.mu.Lock()
	if adapter.challenges[challengeID] == expected {
		adapter.destroyLocked(challengeID, identity.ErrChallengeExpired)
	}
	adapter.mu.Unlock()
}

func cancellationResult(ctx context.Context) error {
	cause := context.Cause(ctx)
	if cause == identity.ErrChallengeExpired {
		return identity.ErrChallengeExpired
	}
	if cause == identity.ErrChallengeNotFound {
		return identity.ErrChallengeNotFound
	}
	return nil
}

func encodeQRImage(value string) (string, error) {
	png, err := qrcode.Encode(value, qrcode.Medium, 280)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

func randomChallengeID() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

var allowedCookieNames = map[string]struct{}{
	"SESSDATA": {}, "bili_jct": {}, "DedeUserID": {}, "DedeUserID__ckMd5": {},
	"sid": {}, "buvid3": {}, "buvid4": {}, "b_nut": {},
}

func loginCookies(response *http.Response, callbackURL string) (map[string]string, bool) {
	cookies := make(map[string]string)
	for _, cookie := range response.Cookies() {
		if _, allowed := allowedCookieNames[cookie.Name]; allowed && cookie.Value != "" {
			cookies[cookie.Name] = cookie.Value
		}
	}
	if parsed, err := url.Parse(callbackURL); err == nil {
		for name := range allowedCookieNames {
			if value := parsed.Query().Get(name); value != "" {
				cookies[name] = value
			}
		}
	}
	return cookies, cookies["SESSDATA"] != ""
}

func cookieHeader(cookies map[string]string) string {
	names := make([]string, 0, len(cookies))
	for name, value := range cookies {
		if _, allowed := allowedCookieNames[name]; allowed && value != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+cookies[name])
	}
	return strings.Join(parts, "; ")
}

func cloneCookies(cookies map[string]string) map[string]string {
	result := make(map[string]string, len(cookies))
	for name, value := range cookies {
		result[name] = value
	}
	return result
}

func destroyCookies(cookies map[string]string) {
	for name := range cookies {
		cookies[name] = ""
		delete(cookies, name)
	}
}
