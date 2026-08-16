package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/adminidentity"
)

const recoveryArchiveFixture = "R1BSQQEQDCAAAIAAAAAACAAAAAEAAAEdsLGys7S1tre4ubq7vL2+v8DBwsPExcbHyMnKy/OsZfWF1Ni/wJbLtaXhn3L2O7UBZh/umY584J8IxZQ+GUUnl/8Nh+dwlW3G4KjyUbDlP2vFi3PsyML32ProgId7mHDRyuhqypPGF36mEh81bubIw9oUqbRDCLXlH7+vOA4AGOfANiolmP1ODOAo65GMpTEd6XzXrCs1Lggs3Suw7aP3Rl6Uc3vxoiHvMtVTqU0qrLPlOfzrZrQNOA573Wn473x7Fw6asWQ56+8jRwCJEiZ9JudESX7gu2uLbcRUC5NZWg+49dRWCzZ3G5aYMZm80zWBlER9ZJUoEgz2pN8ZNf1m5q8uu0y4Oz+2oKpitpoUpNLvbAxa15gNiyuQGG5xQ11uUnhX3gTI7GYQthIpy9/koeG3cr45a8uCQQ=="

func TestNewHTTPServerConfiguresHostedTimeouts(t *testing.T) {
	handler := http.NewServeMux()
	server := newHTTPServer("127.0.0.1:12500", handler)

	if server.Addr != "127.0.0.1:12500" {
		t.Fatalf("Addr = %q, want loopback address", server.Addr)
	}
	if server.Handler != handler {
		t.Fatal("Handler does not match the provided hosted handler")
	}
	if server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v, want 5s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 15*time.Second {
		t.Fatalf("ReadTimeout = %v, want 15s", server.ReadTimeout)
	}
	if server.WriteTimeout != 15*time.Second {
		t.Fatalf("WriteTimeout = %v, want 15s", server.WriteTimeout)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout = %v, want 60s", server.IdleTimeout)
	}
}

func TestServeHTTPReturnsBindErrorBeforeServingOrAnnouncing(t *testing.T) {
	bindErr := errors.New("bind failed")
	var serveCalls atomic.Int32
	server := lifecycleStub{
		serve: func(net.Listener) error {
			serveCalls.Add(1)
			return nil
		},
	}
	var announceCalls int
	var gotNetwork, gotAddress string

	err := serveHTTP(
		context.Background(),
		server,
		"127.0.0.1:12500",
		func(network, address string) (net.Listener, error) {
			gotNetwork, gotAddress = network, address
			return nil, bindErr
		},
		30*time.Second,
		func() { announceCalls++ },
	)

	if !errors.Is(err, bindErr) {
		t.Fatalf("serveHTTP() error = %v, want bind failure", err)
	}
	if gotNetwork != "tcp" || gotAddress != "127.0.0.1:12500" {
		t.Fatalf("listen called with %q %q, want tcp and configured address", gotNetwork, gotAddress)
	}
	if serveCalls.Load() != 0 {
		t.Fatalf("Serve called %d times after bind failure", serveCalls.Load())
	}
	if announceCalls != 0 {
		t.Fatalf("onListening called %d times after bind failure", announceCalls)
	}
}

func TestServeHTTPReturnsUnexpectedServeErrorAndClosesListener(t *testing.T) {
	listener := newTrackedListener()
	serveErr := errors.New("serve failed")
	announced := false
	server := lifecycleStub{
		serve: func(got net.Listener) error {
			if got != listener {
				t.Errorf("Serve listener = %T, want tracked listener", got)
			}
			return serveErr
		},
	}

	err := serveHTTP(
		context.Background(),
		server,
		"127.0.0.1:12500",
		func(string, string) (net.Listener, error) { return listener, nil },
		30*time.Second,
		func() {
			if listener.isClosed() {
				t.Error("listener was closed before listening announcement")
			}
			announced = true
		},
	)

	if !errors.Is(err, serveErr) {
		t.Fatalf("serveHTTP() error = %v, want Serve failure", err)
	}
	if !announced {
		t.Fatal("onListening was not called after successful bind")
	}
	if !listener.isClosed() {
		t.Fatal("listener remained open after Serve failure")
	}
}

func TestServeHTTPShutdownUsesConfiguredDeadlineAndWaitsForServerClosed(t *testing.T) {
	processContext, cancelProcess := context.WithCancel(context.Background())
	cancelProcess()
	listener := newTrackedListener()
	releaseServe := make(chan struct{})
	deadlineRemaining := make(chan time.Duration, 1)
	server := lifecycleStub{
		serve: func(net.Listener) error {
			<-releaseServe
			return http.ErrServerClosed
		},
		shutdown: func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("Shutdown context has no deadline")
				deadlineRemaining <- 0
			} else {
				deadlineRemaining <- time.Until(deadline)
			}
			close(releaseServe)
			return nil
		},
		close: func() error {
			t.Error("Close called after successful graceful shutdown")
			return nil
		},
	}

	err := serveHTTP(
		processContext,
		server,
		"127.0.0.1:12500",
		func(string, string) (net.Listener, error) { return listener, nil },
		30*time.Second,
		func() {},
	)
	if err != nil {
		t.Fatalf("serveHTTP() error = %v", err)
	}

	remaining := <-deadlineRemaining
	if remaining < 29*time.Second || remaining > 30*time.Second {
		t.Fatalf("Shutdown deadline remaining = %v, want approximately 30s", remaining)
	}
	if !listener.isClosed() {
		t.Fatal("listener remained open after graceful shutdown")
	}
}

func TestServeHTTPClosesServerWhenShutdownFails(t *testing.T) {
	processContext, cancelProcess := context.WithCancel(context.Background())
	cancelProcess()
	listener := newTrackedListener()
	shutdownErr := errors.New("shutdown failed")
	releaseServe := make(chan struct{})
	serveExited := make(chan struct{})
	closeCalled := make(chan struct{}, 1)
	server := lifecycleStub{
		serve: func(net.Listener) error {
			<-releaseServe
			close(serveExited)
			return http.ErrServerClosed
		},
		shutdown: func(context.Context) error { return shutdownErr },
		close: func() error {
			closeCalled <- struct{}{}
			close(releaseServe)
			return nil
		},
	}

	err := serveHTTP(
		processContext,
		server,
		"127.0.0.1:12500",
		func(string, string) (net.Listener, error) { return listener, nil },
		30*time.Second,
		func() {},
	)
	if !errors.Is(err, shutdownErr) {
		t.Fatalf("serveHTTP() error = %v, want Shutdown failure", err)
	}
	select {
	case <-closeCalled:
	default:
		t.Fatal("Close was not called after Shutdown failure")
	}
	select {
	case <-serveExited:
	case <-time.After(time.Second):
		t.Fatal("Serve did not exit after Close")
	}
	if !listener.isClosed() {
		t.Fatal("listener remained open after Shutdown failure")
	}
}

func TestServeHTTPTreatsServerClosedAsNormalAndClosesListener(t *testing.T) {
	listener := newTrackedListener()
	server := lifecycleStub{
		serve: func(net.Listener) error { return http.ErrServerClosed },
	}

	err := serveHTTP(
		context.Background(),
		server,
		"127.0.0.1:12500",
		func(string, string) (net.Listener, error) { return listener, nil },
		30*time.Second,
		func() {},
	)
	if err != nil {
		t.Fatalf("serveHTTP() error = %v, want normal ServerClosed result", err)
	}
	if !listener.isClosed() {
		t.Fatal("listener remained open after ServerClosed")
	}
}

func TestRunModeAdminInitPrintsOneTimeSecretsAndNeverStartsHTTP(t *testing.T) {
	initializer := &initializerStub{result: adminidentity.InitializeResult{
		TOTPURI:          "otpauth://totp/GiftPanel:owner?secret=ONCE",
		RecoveryPassword: "12345678901234567890",
	}}
	var output bytes.Buffer
	serveCalls := 0

	err := runMode(
		context.Background(),
		[]string{"admin", "init", "--uid", "32249588", "--email", "owner@example.com"},
		initializer,
		&output,
		func() error { serveCalls++; return nil },
	)
	if err != nil {
		t.Fatalf("runMode() error = %v", err)
	}
	if serveCalls != 0 {
		t.Fatalf("HTTP serve called %d times during local admin init", serveCalls)
	}
	if initializer.uid != "32249588" || initializer.email != "owner@example.com" {
		t.Fatalf("Initialize arguments uid=%q email=%q", initializer.uid, initializer.email)
	}
	for _, secret := range []string{initializer.result.TOTPURI, initializer.result.RecoveryPassword} {
		if got := bytes.Count(output.Bytes(), []byte(secret)); got != 1 {
			t.Fatalf("secret %q appeared %d times in output %q", secret, got, output.String())
		}
	}
	for _, forbidden := range []string{"32249588", "owner@example.com", "MYSQL", "HOSTED_"} {
		if bytes.Contains(output.Bytes(), []byte(forbidden)) {
			t.Fatalf("CLI output exposed %q: %q", forbidden, output.String())
		}
	}
}

func TestRunModeDecryptsGeneratedRecoveryArchiveFromStdinWithoutPasswordArgument(t *testing.T) {
	archive, err := base64.StdEncoding.DecodeString(recoveryArchiveFixture)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "admin-recovery.gpra")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = runModeWithInput(context.Background(), []string{"admin", "recovery", "decrypt", "--archive", archivePath, "--password-stdin"}, nil, strings.NewReader("oaKjpKWmp6ipqqusra6v\n"), &output, nil)
	if err != nil {
		t.Fatalf("decrypt command error = %v", err)
	}
	lines := strings.Fields(output.String())
	if len(lines) != adminidentity.RecoveryCodeCount {
		t.Fatalf("code lines=%d output=%q", len(lines), output.String())
	}
	for index, line := range lines {
		raw := make([]byte, adminidentity.RecoveryCodeBytes)
		for offset := range raw {
			raw[offset] = byte(index*adminidentity.RecoveryCodeBytes + offset + 1)
		}
		if want := base64.RawURLEncoding.EncodeToString(raw); line != want {
			t.Fatalf("code %d=%q want=%q", index, line, want)
		}
	}
	if err := runModeWithInput(context.Background(), []string{"admin", "recovery", "decrypt", "--archive", archivePath, "--password", "oaKjpKWmp6ipqqusra6v"}, nil, strings.NewReader(""), &bytes.Buffer{}, nil); !errors.Is(err, errInvalidCommand) {
		t.Fatalf("password argv error=%v, want invalid command", err)
	}
}

func TestRunDecryptsRecoveryArchiveBeforeConfigurationOrNetworkInitialization(t *testing.T) {
	archive, err := base64.StdEncoding.DecodeString(recoveryArchiveFixture)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	archivePath := filepath.Join(directory, "admin-recovery.gpra")
	passwordPath := filepath.Join(directory, "password.txt")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordPath, []byte("oaKjpKWmp6ipqqusra6v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HOSTED_LISTEN_ADDR", "HOSTED_MYSQL_DSN", "HOSTED_ENCRYPTION_KEY_FILE", "HOSTED_HMAC_KEY_FILE", "HOSTED_SMTP_ADDRESS"} {
		t.Setenv(name, "")
	}
	oldArgs, oldStdout := os.Args, os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"gift-panel-hosted", "admin", "recovery", "decrypt", "--archive", archivePath, "--password-file", passwordPath}
	os.Stdout = writer
	t.Cleanup(func() { os.Args = oldArgs; os.Stdout = oldStdout; _ = reader.Close(); _ = writer.Close() })
	err = run()
	_ = writer.Close()
	os.Stdout = oldStdout
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err != nil {
		t.Fatalf("offline run decrypt error=%v", err)
	}
	if lines := strings.Fields(string(output)); len(lines) != adminidentity.RecoveryCodeCount {
		t.Fatalf("offline run output lines=%d output=%q", len(lines), output)
	}
}

func TestRunModeRepeatedAdminInitFailsClosedWithoutPrintingOrListening(t *testing.T) {
	initializer := &initializerStub{err: adminidentity.ErrAlreadyInitialized}
	var output bytes.Buffer
	serveCalls := 0
	err := runMode(
		context.Background(),
		[]string{"admin", "init", "--uid", "32249588", "--email", "owner@example.com"},
		initializer,
		&output,
		func() error { serveCalls++; return nil },
	)
	if !errors.Is(err, adminidentity.ErrAlreadyInitialized) {
		t.Fatalf("runMode() error = %v", err)
	}
	if output.Len() != 0 || serveCalls != 0 {
		t.Fatalf("failed init output=%q serveCalls=%d", output.String(), serveCalls)
	}
}

func TestRunModeNormalServiceAndInvalidCommandLifecycle(t *testing.T) {
	initializer := &initializerStub{}
	serveCalls := 0
	if err := runMode(context.Background(), nil, initializer, &bytes.Buffer{}, func() error { serveCalls++; return nil }); err != nil {
		t.Fatalf("normal runMode() error = %v", err)
	}
	if serveCalls != 1 || initializer.calls != 0 {
		t.Fatalf("normal mode serveCalls=%d initCalls=%d", serveCalls, initializer.calls)
	}

	serveCalls = 0
	var output bytes.Buffer
	err := runMode(context.Background(), []string{"admin", "init", "--uid", "32249588", "--email", "owner@example.com", "unexpected"}, initializer, &output, func() error { serveCalls++; return nil })
	if !errors.Is(err, errInvalidCommand) || serveCalls != 0 || output.Len() != 0 {
		t.Fatalf("invalid command error=%v serveCalls=%d output=%q", err, serveCalls, output.String())
	}
}

func TestRunModeWithCleanupDoesNotStartCleanupForAdministratorCLI(t *testing.T) {
	initializer := &initializerStub{result: adminidentity.InitializeResult{TOTPURI: "otpauth://pending", RecoveryPassword: "12345678901234567890"}}
	cleanupCalls, serveCalls := 0, 0
	err := runModeWithCleanup(context.Background(), []string{"admin", "init", "--uid", "32249588", "--email", "owner@example.com"}, initializer, strings.NewReader(""), &bytes.Buffer{}, func(context.Context) { cleanupCalls++ }, func() error { serveCalls++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 0 || serveCalls != 0 {
		t.Fatalf("admin CLI cleanupCalls=%d serveCalls=%d", cleanupCalls, serveCalls)
	}
}

func TestRunModeWithCleanupJoinsCleanupBeforeRepositoryClose(t *testing.T) {
	started := make(chan struct{})
	var mu sync.Mutex
	order := make([]string, 0, 2)
	cleanup := func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		mu.Lock()
		order = append(order, "cleanup-exit")
		mu.Unlock()
	}
	err := runModeWithCleanup(context.Background(), nil, &initializerStub{}, strings.NewReader(""), &bytes.Buffer{}, cleanup, func() error { <-started; return nil })
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	order = append(order, "repository-close")
	got := append([]string(nil), order...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "cleanup-exit" || got[1] != "repository-close" {
		t.Fatalf("shutdown order=%v", got)
	}
}

type initializerStub struct {
	result adminidentity.InitializeResult
	err    error
	uid    string
	email  string
	calls  int
}

func (initializer *initializerStub) Initialize(_ context.Context, uid, email string) (adminidentity.InitializeResult, error) {
	initializer.calls++
	initializer.uid, initializer.email = uid, email
	return initializer.result, initializer.err
}

type lifecycleStub struct {
	serve    func(net.Listener) error
	shutdown func(context.Context) error
	close    func() error
}

func (stub lifecycleStub) Serve(listener net.Listener) error {
	if stub.serve == nil {
		panic("unexpected Serve call")
	}
	return stub.serve(listener)
}

func (stub lifecycleStub) Shutdown(ctx context.Context) error {
	if stub.shutdown == nil {
		panic("unexpected Shutdown call")
	}
	return stub.shutdown(ctx)
}

func (stub lifecycleStub) Close() error {
	if stub.close == nil {
		panic("unexpected Close call")
	}
	return stub.close()
}

type trackedListener struct {
	closed    chan struct{}
	closeOnce sync.Once
}

func newTrackedListener() *trackedListener {
	return &trackedListener{closed: make(chan struct{})}
}

func (*trackedListener) Accept() (net.Conn, error) {
	return nil, errors.New("tracked listener does not accept connections")
}

func (listener *trackedListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

func (*trackedListener) Addr() net.Addr {
	return stubAddr("127.0.0.1:12500")
}

func (listener *trackedListener) isClosed() bool {
	select {
	case <-listener.closed:
		return true
	default:
		return false
	}
}

type stubAddr string

func (stubAddr) Network() string { return "tcp" }
func (address stubAddr) String() string {
	return string(address)
}
