package biliqr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
)

func TestAdapterPollsRealBilibiliShapeAndReturnsUIDOnly(t *testing.T) {
	var pollCount int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/generate":
			writeAdapterJSON(response, map[string]any{
				"code": 0,
				"data": map[string]any{"url": "https://account.bilibili.com/scan?qrcode_key=private-qr-key", "qrcode_key": "private-qr-key"},
			})
		case "/poll":
			if request.URL.Query().Get("qrcode_key") != "private-qr-key" {
				t.Fatalf("poll qrcode_key = %q", request.URL.Query().Get("qrcode_key"))
			}
			pollCount++
			if pollCount == 1 {
				writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"code": 86101, "message": "not scanned"}})
				return
			}
			http.SetCookie(response, &http.Cookie{Name: "SESSDATA", Value: "private-session"})
			http.SetCookie(response, &http.Cookie{Name: "bili_jct", Value: "private-csrf"})
			writeAdapterJSON(response, map[string]any{
				"code": 0,
				"data": map[string]any{"code": 0, "url": "https://www.bilibili.com/?DedeUserID=32249588"},
			})
		case "/nav":
			cookie := request.Header.Get("Cookie")
			if !strings.Contains(cookie, "SESSDATA=private-session") || !strings.Contains(cookie, "bili_jct=private-csrf") {
				t.Fatalf("nav Cookie = %q", cookie)
			}
			writeAdapterJSON(response, map[string]any{
				"code": 0,
				"data": map[string]any{"isLogin": true, "mid": 32249588, "uname": "must-not-return", "face": "must-not-return"},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	clock := &mutableAdapterClock{value: now}
	adapter, err := New(Config{
		Client: server.Client(), GenerateEndpoint: server.URL + "/generate", PollEndpoint: server.URL + "/poll", NavEndpoint: server.URL + "/nav",
		Now: clock.Now, Lifetime: 5 * time.Minute,
		EncodeQR: func(value string) (string, error) {
			if !strings.Contains(value, "qrcode_key=private-qr-key") {
				t.Fatalf("encoded QR value = %q", value)
			}
			return "data:image/png;base64,public-image", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := adapter.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if challenge.ID == "" || challenge.QRImage != "data:image/png;base64,public-image" || !challenge.ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("Begin() = %#v", challenge)
	}
	if strings.Contains(challenge.ID, "private-qr-key") {
		t.Fatalf("challenge ID exposed QR key: %q", challenge.ID)
	}

	if _, err := adapter.Poll(context.Background(), challenge.ID); !errors.Is(err, identity.ErrVerificationPending) {
		t.Fatalf("first Poll() error = %v, want pending", err)
	}
	clock.Set(now.Add(2 * time.Second))
	verification, err := adapter.Poll(context.Background(), challenge.ID)
	if err != nil {
		t.Fatalf("second Poll() error = %v", err)
	}
	if verification.UID != "32249588" || !verification.CompletedAt.Equal(now.Add(2*time.Second)) {
		t.Fatalf("verification = %#v", verification)
	}
	encoded, _ := json.Marshal(verification)
	for _, forbidden := range []string{"private-session", "private-csrf", "must-not-return"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("verification exposed %q: %s", forbidden, encoded)
		}
	}
	if _, err := adapter.Poll(context.Background(), challenge.ID); !errors.Is(err, identity.ErrChallengeNotFound) {
		t.Fatalf("Poll() after success error = %v, want forgotten challenge", err)
	}
}

func TestAdapterBeginReturnsOnlyAllowlistedBilibiliVerificationURL(t *testing.T) {
	const verificationURL = "https://passport.bilibili.com/h5-app/passport/login/scan?navhide=1&qrcode_key=public-key"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		writeAdapterJSON(response, map[string]any{
			"code": 0,
			"data": map[string]any{"url": verificationURL, "qrcode_key": "public-key"},
		})
	}))
	defer server.Close()

	adapter, err := New(Config{
		Client: server.Client(), GenerateEndpoint: server.URL, PollEndpoint: server.URL, NavEndpoint: server.URL,
		EncodeQR: func(value string) (string, error) {
			if value != verificationURL {
				t.Fatalf("encoded URL = %q", value)
			}
			return "data:image/png;base64,qr", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := adapter.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if challenge.VerificationURL != verificationURL {
		t.Fatalf("VerificationURL = %q, want %q", challenge.VerificationURL, verificationURL)
	}
}

func TestAdapterBeginRejectsUntrustedVerificationURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "http", url: "http://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=key"},
		{name: "userinfo", url: "https://user@passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=key"},
		{name: "fragment", url: "https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=key#secret"},
		{name: "unknown host", url: "https://example.test/h5-app/passport/login/scan?qrcode_key=key"},
		{name: "unknown path", url: "https://passport.bilibili.com/other?qrcode_key=key"},
		{name: "missing key", url: "https://passport.bilibili.com/h5-app/passport/login/scan"},
		{name: "duplicate key", url: "https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=one&qrcode_key=two"},
		{name: "credential query", url: "https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=key&SESSDATA=secret"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"url": test.url, "qrcode_key": "key"}})
			}))
			defer server.Close()
			adapter, err := New(Config{Client: server.Client(), GenerateEndpoint: server.URL, PollEndpoint: server.URL, NavEndpoint: server.URL, EncodeQR: func(string) (string, error) { return "qr", nil }})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.Begin(context.Background()); !errors.Is(err, identity.ErrVerificationFailed) {
				t.Fatalf("Begin() error = %v, want verification failed", err)
			}
		})
	}
}

func TestAdapterConsumeCredentialRetainsOnlyUntilConsumerSucceeds(t *testing.T) {
	var pollCalls int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/generate":
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"url": testVerificationURL("service-qr-key"), "qrcode_key": "service-qr-key"}})
		case "/poll":
			pollCalls++
			http.SetCookie(response, &http.Cookie{Name: "SESSDATA", Value: "service-cookie"})
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"code": 0, "url": "https://example.test/"}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	adapter, err := New(Config{Client: server.Client(), GenerateEndpoint: server.URL + "/generate", PollEndpoint: server.URL + "/poll", NavEndpoint: server.URL + "/nav", EncodeQR: func(string) (string, error) { return "qr", nil }})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := adapter.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	consumerFailure := errors.New("transaction unavailable")
	if err := adapter.ConsumeCredential(context.Background(), challenge.ID, func(_ context.Context, cookie []byte) error {
		if !strings.Contains(string(cookie), "SESSDATA=service-cookie") {
			t.Fatalf("credential callback = %q", cookie)
		}
		return consumerFailure
	}); !errors.Is(err, consumerFailure) {
		t.Fatalf("first ConsumeCredential() error = %v, want consumer failure", err)
	}
	if err := adapter.ConsumeCredential(context.Background(), challenge.ID, func(_ context.Context, cookie []byte) error {
		if !strings.Contains(string(cookie), "SESSDATA=service-cookie") {
			t.Fatalf("retry callback = %q", cookie)
		}
		return nil
	}); err != nil {
		t.Fatalf("retry ConsumeCredential() error = %v", err)
	}
	if pollCalls != 1 {
		t.Fatalf("poll calls = %d, want completed credential retry without a new upstream poll", pollCalls)
	}
	if _, err := adapter.Poll(context.Background(), challenge.ID); !errors.Is(err, identity.ErrChallengeNotFound) {
		t.Fatalf("successful callback kept credential: %v", err)
	}
}

func TestAdapterConsumeCredentialReservesCompletedChallengeForOneConsumer(t *testing.T) {
	adapter := &Adapter{now: time.Now, challenges: map[string]*pendingChallenge{"challenge": {expiresAt: time.Now().Add(time.Minute), cookies: map[string]string{"SESSDATA": "secret"}}}}
	started, release := make(chan struct{}), make(chan struct{})
	var calls sync.WaitGroup
	calls.Add(1)
	go func() {
		defer calls.Done()
		if err := adapter.ConsumeCredential(context.Background(), "challenge", func(context.Context, []byte) error { close(started); <-release; return nil }); err != nil {
			t.Errorf("winner error=%v", err)
		}
	}()
	<-started
	if err := adapter.ConsumeCredential(context.Background(), "challenge", func(context.Context, []byte) error { t.Fatal("second consumer ran"); return nil }); !errors.Is(err, identity.ErrVerificationPending) {
		t.Fatalf("second consumer error=%v", err)
	}
	close(release)
	calls.Wait()
}

func TestAdapterPollDuringCredentialConsumptionStaysPendingAndPreservesChallenge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"code": 86038}})
	}))
	defer server.Close()
	state := &pendingChallenge{expiresAt: time.Now().Add(time.Minute), cookies: map[string]string{"SESSDATA": "secret"}}
	adapter := &Adapter{client: server.Client(), pollEndpoint: server.URL, now: time.Now, challenges: map[string]*pendingChallenge{"challenge": state}}
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- adapter.ConsumeCredential(context.Background(), "challenge", func(context.Context, []byte) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	verification, err := adapter.Poll(context.Background(), "challenge")
	if verification.UID != "" || !errors.Is(err, identity.ErrVerificationPending) {
		t.Fatalf("Poll during credential consumption=%#v, %v", verification, err)
	}
	adapter.mu.Lock()
	current, exists := adapter.challenges["challenge"]
	stillConsuming := exists && current == state && state.consuming
	adapter.mu.Unlock()
	if !stillConsuming {
		t.Fatal("Poll terminally consumed or forgot the reserved challenge")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("reserved credential consumer error=%v", err)
	}
}

func TestAdapterConsumeCredentialDuringOrdinaryPollStaysPending(t *testing.T) {
	pollStarted := make(chan struct{})
	releasePoll := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		close(pollStarted)
		<-releasePoll
		writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"code": 86101}})
	}))
	defer server.Close()
	state := &pendingChallenge{qrKey: "ordinary-key", expiresAt: time.Now().Add(time.Minute)}
	adapter := &Adapter{client: server.Client(), pollEndpoint: server.URL, now: time.Now, challenges: map[string]*pendingChallenge{"challenge": state}}
	type pollResult struct {
		verification identity.Verification
		err          error
	}
	pollDone := make(chan pollResult, 1)
	go func() {
		verification, err := adapter.Poll(context.Background(), "challenge")
		pollDone <- pollResult{verification: verification, err: err}
	}()
	<-pollStarted
	callbackRan := false
	err := adapter.ConsumeCredential(context.Background(), "challenge", func(context.Context, []byte) error {
		callbackRan = true
		return nil
	})
	if !errors.Is(err, identity.ErrVerificationPending) || callbackRan {
		t.Fatalf("ConsumeCredential during Poll error=%v callbackRan=%v", err, callbackRan)
	}
	close(releasePoll)
	outcome := <-pollDone
	if outcome.verification.UID != "" || !errors.Is(outcome.err, identity.ErrVerificationPending) {
		t.Fatalf("ordinary Poll outcome=%#v, %v", outcome.verification, outcome.err)
	}
	adapter.mu.Lock()
	_, exists := adapter.challenges["challenge"]
	adapter.mu.Unlock()
	if !exists {
		t.Fatal("pending ordinary Poll lost challenge after competing ConsumeCredential")
	}
}

func TestAdapterConsumeCredentialFailureCanRetryOnlyWithinAbsoluteTTL(t *testing.T) {
	clock := &mutableAdapterClock{value: time.Now()}
	state := &pendingChallenge{
		expiresAt: clock.Now().Add(time.Minute),
		cookies:   map[string]string{"SESSDATA": "secret"},
	}
	adapter := &Adapter{now: clock.Now, challenges: map[string]*pendingChallenge{"challenge": state}}
	failure := errors.New("temporary persistence failure")
	if err := adapter.ConsumeCredential(context.Background(), "challenge", func(context.Context, []byte) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("first callback error=%v", err)
	}
	adapter.mu.Lock()
	releasedForRetry := !state.consuming && state.cancel == nil
	adapter.mu.Unlock()
	if !releasedForRetry {
		t.Fatal("valid failed callback retained its consumer reservation")
	}
	clock.Set(clock.Now().Add(59 * time.Second))
	if err := adapter.ConsumeCredential(context.Background(), "challenge", func(context.Context, []byte) error { return nil }); err != nil {
		t.Fatalf("retry before absolute TTL error=%v", err)
	}
	if _, exists := adapter.challenges["challenge"]; exists {
		t.Fatal("successful retry retained challenge")
	}
}

func TestAdapterConsumeCredentialPollCompletionAfterAbsoluteTTLDoesNotRunCallback(t *testing.T) {
	clock := &mutableAdapterClock{value: time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)}
	pollStarted := make(chan struct{})
	releasePoll := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/generate":
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"url": testVerificationURL("service-key"), "qrcode_key": "service-key"}})
		case "/poll":
			close(pollStarted)
			<-releasePoll
			http.SetCookie(response, &http.Cookie{Name: "SESSDATA", Value: "service-cookie"})
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"code": 0, "url": "https://example.test/"}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	adapter, err := New(Config{
		Client: server.Client(), GenerateEndpoint: server.URL + "/generate", PollEndpoint: server.URL + "/poll", NavEndpoint: server.URL + "/nav",
		Now: clock.Now, Lifetime: time.Minute, EncodeQR: func(string) (string, error) { return "qr", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := adapter.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	callbackRan := false
	result := make(chan error, 1)
	go func() {
		result <- adapter.ConsumeCredential(context.Background(), challenge.ID, func(context.Context, []byte) error {
			callbackRan = true
			return nil
		})
	}()
	<-pollStarted
	clock.Set(challenge.ExpiresAt)
	close(releasePoll)
	if err := <-result; !errors.Is(err, identity.ErrChallengeExpired) {
		t.Fatalf("completion at absolute TTL error=%v", err)
	}
	if callbackRan {
		t.Fatal("expired poll completion ran credential callback")
	}
	if _, exists := adapter.challenges[challenge.ID]; exists {
		t.Fatal("expired poll completion retained credential")
	}
}

func TestAdapterConsumeCredentialExpiryDuringCallbackCannotCommitSuccess(t *testing.T) {
	clock := &mutableAdapterClock{value: time.Now()}
	adapter := &Adapter{now: clock.Now, challenges: make(map[string]*pendingChallenge)}
	state := &pendingChallenge{
		expiresAt: clock.Now().Add(time.Minute),
		cookies:   map[string]string{"SESSDATA": "secret"},
	}
	adapter.challenges["challenge"] = state
	started := make(chan struct{})
	commit := make(chan struct{})
	result := make(chan error, 1)
	sideEffect := false
	go func() {
		result <- adapter.ConsumeCredential(context.Background(), "challenge", func(consumerContext context.Context, _ []byte) error {
			deadline, ok := consumerContext.Deadline()
			if !ok || !deadline.Equal(state.expiresAt) {
				return errors.New("consumer context missing absolute challenge deadline")
			}
			close(started)
			select {
			case <-consumerContext.Done():
				return context.Cause(consumerContext)
			case <-commit:
				sideEffect = true
				return nil
			}
		})
	}()
	<-started
	clock.Set(state.expiresAt)
	adapter.expireChallenge("challenge", state)
	if _, exists := adapter.challenges["challenge"]; exists {
		t.Fatal("TTL did not destroy in-flight credential")
	}
	if err := <-result; !errors.Is(err, identity.ErrChallengeExpired) {
		t.Fatalf("expiry during callback error=%v, want challenge expired", err)
	}
	if sideEffect {
		t.Fatal("expired consumer context allowed transaction side effect")
	}
}

func TestAdapterConsumeCredentialCloseDuringCallbackCannotCommitSuccess(t *testing.T) {
	adapter := &Adapter{now: time.Now, challenges: map[string]*pendingChallenge{
		"challenge": {expiresAt: time.Now().Add(time.Minute), cookies: map[string]string{"SESSDATA": "secret"}},
	}}
	started := make(chan struct{})
	commit := make(chan struct{})
	result := make(chan error, 1)
	sideEffect := false
	go func() {
		result <- adapter.ConsumeCredential(context.Background(), "challenge", func(consumerContext context.Context, _ []byte) error {
			close(started)
			select {
			case <-consumerContext.Done():
				return context.Cause(consumerContext)
			case <-commit:
				sideEffect = true
				return nil
			}
		})
	}()
	<-started
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, identity.ErrChallengeNotFound) {
		t.Fatalf("Close during callback error=%v, want challenge not found", err)
	}
	if sideEffect {
		t.Fatal("closed consumer context allowed transaction side effect")
	}
}

func TestAdapterExpiresForgetsAndClosesAllChallenges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/generate" {
			http.NotFound(response, request)
			return
		}
		key := request.URL.Query().Get("key")
		if key == "" {
			key = "generated-key"
		}
		writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"url": testVerificationURL(key), "qrcode_key": key}})
	}))
	defer server.Close()

	clock := time.Date(2026, 8, 16, 12, 30, 0, 0, time.UTC)
	sequence := 0
	adapter, err := New(Config{
		Client: server.Client(), GenerateEndpoint: server.URL + "/generate", PollEndpoint: server.URL + "/poll", NavEndpoint: server.URL + "/nav",
		Now: func() time.Time { return clock }, Lifetime: 5 * time.Minute,
		EncodeQR: func(string) (string, error) { sequence++; return "qr", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	expiring, err := adapter.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(5 * time.Minute)
	if _, err := adapter.Poll(context.Background(), expiring.ID); !errors.Is(err, identity.ErrChallengeExpired) {
		t.Fatalf("expired Poll() error = %v", err)
	}
	if _, err := adapter.Poll(context.Background(), expiring.ID); !errors.Is(err, identity.ErrChallengeNotFound) {
		t.Fatalf("expired challenge remained in memory: %v", err)
	}

	clock = clock.Add(-time.Minute)
	forgotten, _ := adapter.Begin(context.Background())
	adapter.Forget(forgotten.ID)
	if _, err := adapter.Poll(context.Background(), forgotten.ID); !errors.Is(err, identity.ErrChallengeNotFound) {
		t.Fatalf("Forget() left challenge in memory: %v", err)
	}

	first, _ := adapter.Begin(context.Background())
	second, _ := adapter.Begin(context.Background())
	if err := adapter.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	for _, challenge := range []identity.Challenge{first, second} {
		if _, err := adapter.Poll(context.Background(), challenge.ID); !errors.Is(err, identity.ErrChallengeNotFound) {
			t.Fatalf("Close() left challenge %q in memory: %v", challenge.ID, err)
		}
	}
}

func TestAdapterTerminalBilibiliResultsAlwaysForgetSecrets(t *testing.T) {
	tests := []struct {
		name      string
		poll      map[string]any
		setCookie bool
		nav       map[string]any
		want      error
	}{
		{name: "expired", poll: map[string]any{"code": 0, "data": map[string]any{"code": 86038}}, want: identity.ErrChallengeExpired},
		{name: "unknown terminal", poll: map[string]any{"code": 0, "data": map[string]any{"code": 12345}}, want: identity.ErrVerificationFailed},
		{name: "success without cookie", poll: map[string]any{"code": 0, "data": map[string]any{"code": 0, "url": "https://example.test/"}}, want: identity.ErrVerificationFailed},
		{name: "nav rejects cookie", poll: map[string]any{"code": 0, "data": map[string]any{"code": 0, "url": "https://example.test/"}}, setCookie: true, nav: map[string]any{"code": -101, "data": map[string]any{"isLogin": false}}, want: identity.ErrVerificationFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/generate":
					writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"url": testVerificationURL("secret-key"), "qrcode_key": "secret-key"}})
				case "/poll":
					if test.setCookie {
						http.SetCookie(response, &http.Cookie{Name: "SESSDATA", Value: "secret-cookie"})
					}
					writeAdapterJSON(response, test.poll)
				case "/nav":
					writeAdapterJSON(response, test.nav)
				}
			}))
			defer server.Close()
			adapter, err := New(Config{Client: server.Client(), GenerateEndpoint: server.URL + "/generate", PollEndpoint: server.URL + "/poll", NavEndpoint: server.URL + "/nav", EncodeQR: func(string) (string, error) { return "qr", nil }})
			if err != nil {
				t.Fatal(err)
			}
			challenge, err := adapter.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.Poll(context.Background(), challenge.ID); !errors.Is(err, test.want) {
				t.Fatalf("Poll() error = %v, want %v", err, test.want)
			}
			if _, err := adapter.Poll(context.Background(), challenge.ID); !errors.Is(err, identity.ErrChallengeNotFound) {
				t.Fatalf("terminal challenge remained in memory: %v", err)
			}
		})
	}
}

func TestAdapterCloseDuringIdentityLookupCannotReturnUID(t *testing.T) {
	navStarted := make(chan struct{})
	emergencyRelease := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/generate":
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"url": testVerificationURL("secret-key"), "qrcode_key": "secret-key"}})
		case "/poll":
			http.SetCookie(response, &http.Cookie{Name: "SESSDATA", Value: "secret-cookie"})
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"code": 0, "url": "https://example.test/"}})
		case "/nav":
			close(navStarted)
			select {
			case <-request.Context().Done():
			case <-emergencyRelease:
				writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"isLogin": true, "mid": 32249588}})
			}
		}
	}))
	defer server.Close()
	defer close(emergencyRelease)
	adapter, err := New(Config{Client: server.Client(), GenerateEndpoint: server.URL + "/generate", PollEndpoint: server.URL + "/poll", NavEndpoint: server.URL + "/nav", EncodeQR: func(string) (string, error) { return "qr", nil }})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := adapter.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	type pollOutcome struct {
		verification identity.Verification
		err          error
	}
	outcomes := make(chan pollOutcome, 1)
	go func() {
		verification, err := adapter.Poll(context.Background(), challenge.ID)
		outcomes <- pollOutcome{verification: verification, err: err}
	}()
	<-navStarted
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	var outcome pollOutcome
	select {
	case outcome = <-outcomes:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the in-flight Bilibili identity request")
	}
	if outcome.verification.UID != "" || !errors.Is(outcome.err, identity.ErrChallengeNotFound) {
		t.Fatalf("Poll() after Close = %#v, %v; want no UID", outcome.verification, outcome.err)
	}
}

func TestAdapterTTLExpiryCancelsInFlightIdentityRequest(t *testing.T) {
	navStarted := make(chan struct{})
	emergencyRelease := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/generate":
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"url": testVerificationURL("ttl-key"), "qrcode_key": "ttl-key"}})
		case "/poll":
			http.SetCookie(response, &http.Cookie{Name: "SESSDATA", Value: "ttl-cookie"})
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"code": 0, "url": "https://example.test/"}})
		case "/nav":
			close(navStarted)
			select {
			case <-request.Context().Done():
			case <-emergencyRelease:
				writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"isLogin": true, "mid": 32249588}})
			}
		}
	}))
	defer server.Close()
	defer close(emergencyRelease)
	adapter, err := New(Config{
		Client: server.Client(), GenerateEndpoint: server.URL + "/generate", PollEndpoint: server.URL + "/poll", NavEndpoint: server.URL + "/nav",
		Lifetime: 25 * time.Millisecond, EncodeQR: func(string) (string, error) { return "qr", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := adapter.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type pollOutcome struct {
		verification identity.Verification
		err          error
	}
	outcomes := make(chan pollOutcome, 1)
	go func() {
		verification, err := adapter.Poll(context.Background(), challenge.ID)
		outcomes <- pollOutcome{verification: verification, err: err}
	}()
	<-navStarted
	select {
	case outcome := <-outcomes:
		if outcome.verification.UID != "" || !errors.Is(outcome.err, identity.ErrChallengeExpired) {
			t.Fatalf("Poll() after TTL = %#v, %v", outcome.verification, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("TTL expiry did not cancel the in-flight Bilibili identity request")
	}
}

func TestAdapterActivelyDeletesAbandonedQRKeyAtTTL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/generate" {
			http.NotFound(response, request)
			return
		}
		writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"url": testVerificationURL("abandoned-secret-key"), "qrcode_key": "abandoned-secret-key"}})
	}))
	defer server.Close()
	adapter, err := New(Config{
		Client: server.Client(), GenerateEndpoint: server.URL + "/generate", PollEndpoint: server.URL + "/poll", NavEndpoint: server.URL + "/nav",
		Now: func() time.Time { return time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC) }, Lifetime: 25 * time.Millisecond,
		EncodeQR: func(string) (string, error) { return "qr", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		adapter.mu.Lock()
		remaining := len(adapter.challenges)
		adapter.mu.Unlock()
		if remaining == 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("QR key remained in adapter map past TTL: entries=%d", remaining)
		case <-ticker.C:
		}
	}
}

func TestAdapterThrottlesEachChallengeToBilibiliPollingInterval(t *testing.T) {
	initial := time.Date(2026, 8, 16, 13, 5, 0, 0, time.UTC)
	clock := &mutableAdapterClock{value: initial}
	pollCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/generate":
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"url": testVerificationURL("throttled-key"), "qrcode_key": "throttled-key"}})
		case "/poll":
			pollCount++
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"code": 86101}})
		}
	}))
	defer server.Close()
	adapter, err := New(Config{
		Client: server.Client(), GenerateEndpoint: server.URL + "/generate", PollEndpoint: server.URL + "/poll", NavEndpoint: server.URL + "/nav",
		Now: clock.Now, Lifetime: time.Minute, PollInterval: 2 * time.Second, EncodeQR: func(string) (string, error) { return "qr", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := adapter.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := adapter.Poll(context.Background(), challenge.ID); !errors.Is(err, identity.ErrVerificationPending) {
			t.Fatalf("Poll() attempt %d error = %v", attempt+1, err)
		}
	}
	if pollCount != 1 {
		t.Fatalf("immediate Poll HTTP calls = %d, want 1", pollCount)
	}
	clock.Set(initial.Add(2 * time.Second))
	if _, err := adapter.Poll(context.Background(), challenge.ID); !errors.Is(err, identity.ErrVerificationPending) {
		t.Fatalf("Poll() after interval error = %v", err)
	}
	if pollCount != 2 {
		t.Fatalf("Poll HTTP calls after interval = %d, want 2", pollCount)
	}
}

func TestAdapterPollCompletionAfterLogicalExpiryCannotReturnUID(t *testing.T) {
	initial := time.Date(2026, 8, 16, 13, 10, 0, 0, time.UTC)
	clock := &mutableAdapterClock{value: initial}
	navStarted := make(chan struct{})
	releaseNav := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/generate":
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"url": testVerificationURL("expiring-key"), "qrcode_key": "expiring-key"}})
		case "/poll":
			http.SetCookie(response, &http.Cookie{Name: "SESSDATA", Value: "expiring-cookie"})
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"code": 0, "url": "https://example.test/"}})
		case "/nav":
			close(navStarted)
			<-releaseNav
			writeAdapterJSON(response, map[string]any{"code": 0, "data": map[string]any{"isLogin": true, "mid": 32249588}})
		}
	}))
	defer server.Close()
	adapter, err := New(Config{
		Client: server.Client(), GenerateEndpoint: server.URL + "/generate", PollEndpoint: server.URL + "/poll", NavEndpoint: server.URL + "/nav",
		Now: clock.Now, Lifetime: time.Second, EncodeQR: func(string) (string, error) { return "qr", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := adapter.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type pollOutcome struct {
		verification identity.Verification
		err          error
	}
	outcomes := make(chan pollOutcome, 1)
	go func() {
		verification, err := adapter.Poll(context.Background(), challenge.ID)
		outcomes <- pollOutcome{verification: verification, err: err}
	}()
	<-navStarted
	clock.Set(initial.Add(2 * time.Second))
	close(releaseNav)
	outcome := <-outcomes
	if outcome.verification.UID != "" || !errors.Is(outcome.err, identity.ErrChallengeExpired) {
		t.Fatalf("Poll() after logical expiry = %#v, %v", outcome.verification, outcome.err)
	}
}

func writeAdapterJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(value)
}

func testVerificationURL(key string) string {
	return "https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=" + url.QueryEscape(key)
}

type mutableAdapterClock struct {
	mu    sync.Mutex
	value time.Time
}

func (clock *mutableAdapterClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.value
}

func (clock *mutableAdapterClock) Set(value time.Time) {
	clock.mu.Lock()
	clock.value = value
	clock.mu.Unlock()
}
