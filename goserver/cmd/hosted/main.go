package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"bilibili-live-gift-panel/internal/hosted/adminidentity"
	"bilibili-live-gift-panel/internal/hosted/app"
	"bilibili-live-gift-panel/internal/hosted/biligateway"
	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/identity/biliqr"
	"bilibili-live-gift-panel/internal/hosted/invitation"
	"bilibili-live-gift-panel/internal/hosted/migration"
	"bilibili-live-gift-panel/internal/hosted/platform"
	"bilibili-live-gift-panel/internal/hosted/security"
	"bilibili-live-gift-panel/internal/hosted/store/mysqlstore"
)

const shutdownTimeout = 30 * time.Second

var errInvalidCommand = errors.New("invalid hosted command")

type adminInitializer interface {
	Initialize(context.Context, string, string) (adminidentity.InitializeResult, error)
}

type serverLifecycle interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}

type listenFunc func(network, address string) (net.Listener, error)

func main() {
	if err := run(); err != nil {
		slog.Error("hosted service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if args := os.Args[1:]; len(args) >= 3 && args[0] == "admin" && args[1] == "recovery" && args[2] == "decrypt" {
		return runRecoveryDecrypt(args[3:], os.Stdin, os.Stdout)
	}
	processContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	config, err := platform.Load()
	if err != nil {
		return fmt.Errorf("load hosted configuration: %w", err)
	}

	store, err := mysqlstore.Open(processContext, config.MySQLDSN)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(processContext); err != nil {
		return fmt.Errorf("migrate hosted database: %w", err)
	}

	keys, err := loadHostedKeyring(config.EncryptionKeyFile, config.HMACKeyFile)
	if err != nil {
		return err
	}
	// All hosted SQL modules borrow the Store-owned pool. Store is the only
	// database Close owner for the process.
	adminRepository := adminidentity.NewRepository(store.Database())
	sender, err := adminidentity.NewSMTPSender(adminidentity.SMTPConfig{
		Address: config.SMTPAddress, Host: config.SMTPHost, Username: config.SMTPUsername,
		Password: config.SMTPPassword, From: config.SMTPFrom, Mode: adminidentity.SMTPMode(config.SMTPMode),
		AllowInsecureLocalhost: config.SMTPAllowInsecureLocalhost,
	})
	if err != nil {
		return errors.New("configure administrator mail delivery")
	}
	verifier, err := biliqr.New(biliqr.Config{})
	if err != nil {
		return errors.New("configure Bilibili verification")
	}
	defer verifier.Close()
	adminService, err := adminidentity.NewService(adminRepository, keys, verifier, sender, adminidentity.ServiceOptions{})
	if err != nil {
		return errors.New("configure administrator identity")
	}
	return runModeWithCleanup(processContext, os.Args[1:], adminService, os.Stdin, os.Stdout, func(cleanupContext context.Context) {
		adminService.RunHandoffCleanup(cleanupContext, time.Minute)
	}, func() error {
		resolver, err := identity.NewTrustedProxyClientIPResolver([]string{"127.0.0.1/32", "::1/128"})
		if err != nil {
			return errors.New("configure hosted client address policy")
		}
		limiter := newLocalLimiter(time.Now)
		identityService, err := identity.NewService(identity.NewRepository(store.Database()), keys, verifier, identity.ServiceOptions{})
		if err != nil {
			return errors.New("configure hosted identity")
		}
		// The identity service owns process-local intents and must close before
		// the shared verifier and Store-owned database pool in the outer scope.
		defer identityService.Close()
		identityHTTP, err := identity.NewHTTPHandler(identityService, identity.HTTPOptions{
			AllowedOrigin: config.AdminAllowedOrigin, CSRFToken: config.AdminCSRFToken,
			Limiter: limiter, ClientIP: resolver,
		})
		if err != nil {
			return errors.New("configure hosted identity HTTP")
		}
		invitationService, err := invitation.NewService(store.Database(), keys, identityService, invitation.ServiceOptions{})
		if err != nil {
			return errors.New("configure hosted invitations")
		}
		invitationHTTP, err := invitation.NewHTTPHandler(invitationService, invitation.HTTPOptions{
			AllowedOrigin: config.AdminAllowedOrigin, CSRFToken: config.AdminCSRFToken,
			Limiter: limiter, ClientIP: resolver, Authenticate: identityHTTP.Authenticate,
		})
		if err != nil {
			return errors.New("configure hosted invitation HTTP")
		}
		configurationService := configuration.NewService(configuration.NewRepository(store.Database()), time.Now)
		configurationHTTP, err := configuration.NewHTTPHandler(configurationService, configuration.HTTPOptions{
			AllowedOrigin: config.AdminAllowedOrigin, CSRFToken: config.AdminCSRFToken,
			Limiter: limiter, ClientIP: resolver, Authenticate: identityHTTP.Authenticate,
		})
		if err != nil {
			return errors.New("configure hosted configuration HTTP")
		}
		migrationService := migration.NewService(migration.NewRepository(store.Database()), time.Now)
		migrationHTTP, err := migration.NewHTTPHandler(migrationService, identityService, migration.HTTPOptions{
			AllowedOrigin: config.AdminAllowedOrigin, CSRFToken: config.AdminCSRFToken,
			Limiter: limiter, ClientIP: resolver, Authenticate: identityHTTP.Authenticate,
		})
		if err != nil {
			return errors.New("configure hosted migration HTTP")
		}
		biliService, err := biligateway.NewService(verifier, biligateway.NewCredentialStore(store.Database(), keys, time.Now), adminService)
		if err != nil {
			return errors.New("configure Bilibili service credential")
		}
		biliServiceHTTP, err := biligateway.NewHTTPHandler(biliService, biligateway.HTTPOptions{
			AllowedOrigin: config.AdminAllowedOrigin, CSRFToken: config.AdminCSRFToken,
			Limiter: limiter, ClientIP: resolver,
		})
		if err != nil {
			return errors.New("configure Bilibili service HTTP")
		}
		adminHTTP, err := adminidentity.NewHTTPHandler(adminService, adminidentity.HTTPOptions{
			AllowedOrigin: config.AdminAllowedOrigin, CSRFToken: config.AdminCSRFToken,
			Limiter: limiter, ClientIP: resolver,
		})
		if err != nil {
			return errors.New("configure administrator HTTP")
		}
		server := newHTTPServer(config.ListenAddr, composeHostedHTTP(store, identityHTTP, adminHTTP, invitationHTTP, configurationHTTP, migrationHTTP, biliServiceHTTP, config.AdminCSRFToken))
		return serveHTTP(
			processContext,
			server,
			config.ListenAddr,
			net.Listen,
			shutdownTimeout,
			func() { slog.Info("hosted service listening", "address", config.ListenAddr) },
		)
	})
}

type hostedHealthChecker interface {
	Health(context.Context) error
}

func composeHostedHTTP(database hostedHealthChecker, auth, admin, invitations, configurationHTTP, migrationHTTP, biliServiceHTTP http.Handler, csrfToken string) http.Handler {
	return app.New(app.Dependencies{DB: database, Auth: auth, Admin: admin, Invitation: invitations, Configuration: configurationHTTP, Migration: migrationHTTP, BiliService: biliServiceHTTP, CSRFToken: csrfToken})
}

func loadHostedKeyring(encryptionKeyFile, hmacKeyFile string) (security.Keyring, error) {
	encryptionKey, err := os.ReadFile(encryptionKeyFile)
	if err != nil {
		return security.Keyring{}, errors.New("load hosted key material")
	}
	defer clear(encryptionKey)
	hmacKey, err := os.ReadFile(hmacKeyFile)
	if err != nil {
		return security.Keyring{}, errors.New("load hosted key material")
	}
	defer clear(hmacKey)
	keys, err := security.NewKeyring(1, encryptionKey, hmacKey)
	if err != nil {
		return security.Keyring{}, errors.New("load hosted key material")
	}
	return keys, nil
}

type limiterBucket struct {
	windowStart time.Time
	count       int
}

type localLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]limiterBucket
}

func newLocalLimiter(now func() time.Time) *localLimiter {
	return &localLimiter{now: now, buckets: make(map[string]limiterBucket)}
}

func (limiter *localLimiter) Allow(ctx context.Context, scope identity.LimitScope, key string) bool {
	if limiter == nil || ctx.Err() != nil || key == "" {
		return false
	}
	limit := 60
	switch scope {
	case identity.LimitGlobal:
		limit = 60
	case identity.LimitPerIP:
		limit = 20
	case identity.LimitPerChallenge:
		limit = 12
	default:
		return false
	}
	now := limiter.now()
	bucketKey := string(scope) + "\x00" + key
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	for candidate, bucket := range limiter.buckets {
		if now.Sub(bucket.windowStart) >= time.Minute {
			delete(limiter.buckets, candidate)
		}
	}
	bucket := limiter.buckets[bucketKey]
	if bucket.windowStart.IsZero() || now.Sub(bucket.windowStart) >= time.Minute {
		bucket = limiterBucket{windowStart: now}
	}
	if bucket.count >= limit {
		limiter.buckets[bucketKey] = bucket
		return false
	}
	bucket.count++
	limiter.buckets[bucketKey] = bucket
	return true
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func runMode(ctx context.Context, args []string, initializer adminInitializer, output io.Writer, serve func() error) error {
	return runModeWithInput(ctx, args, initializer, strings.NewReader(""), output, serve)
}

func runModeWithInput(ctx context.Context, args []string, initializer adminInitializer, input io.Reader, output io.Writer, serve func() error) error {
	if len(args) == 0 {
		if serve == nil {
			return errInvalidCommand
		}
		return serve()
	}
	if len(args) >= 3 && args[0] == "admin" && args[1] == "recovery" && args[2] == "decrypt" {
		return runRecoveryDecrypt(args[3:], input, output)
	}
	if len(args) < 2 || args[0] != "admin" || args[1] != "init" || initializer == nil || output == nil {
		return errInvalidCommand
	}
	flags := flag.NewFlagSet("admin init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	uid := flags.String("uid", "", "administrator Bilibili UID")
	email := flags.String("email", "", "administrator recovery email")
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *uid == "" || *email == "" {
		return errInvalidCommand
	}
	result, err := initializer.Initialize(ctx, *uid, *email)
	if err != nil {
		return err
	}
	if result.TOTPURI == "" || len(result.RecoveryPassword) != 20 {
		return adminidentity.ErrUnavailable
	}
	if _, err := fmt.Fprintf(output, "TOTP URI: %s\nRecovery package password: %s\n", result.TOTPURI, result.RecoveryPassword); err != nil {
		return errors.New("write administrator initialization result")
	}
	return nil
}

func runModeWithCleanup(ctx context.Context, args []string, initializer adminInitializer, input io.Reader, output io.Writer, cleanup func(context.Context), serve func() error) error {
	if ctx == nil || cleanup == nil || serve == nil {
		return errInvalidCommand
	}
	return runModeWithInput(ctx, args, initializer, input, output, func() error {
		cleanupContext, cancelCleanup := context.WithCancel(ctx)
		cleanupDone := make(chan struct{})
		go func() {
			defer close(cleanupDone)
			cleanup(cleanupContext)
		}()
		defer func() {
			cancelCleanup()
			<-cleanupDone
		}()
		return serve()
	})
}

func runRecoveryDecrypt(args []string, input io.Reader, output io.Writer) error {
	if input == nil || output == nil {
		return errInvalidCommand
	}
	flags := flag.NewFlagSet("admin recovery decrypt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	archivePath := flags.String("archive", "", "encrypted recovery archive")
	passwordStdin := flags.Bool("password-stdin", false, "read password from standard input")
	passwordFile := flags.String("password-file", "", "read password from file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *archivePath == "" || (*passwordStdin == (*passwordFile != "")) {
		return errInvalidCommand
	}
	archive, err := readBoundedFile(*archivePath, 4<<20)
	if err != nil || len(archive) == 0 {
		return adminidentity.ErrArchiveAuthentication
	}
	var passwordBytes []byte
	if *passwordStdin {
		passwordBytes, err = io.ReadAll(io.LimitReader(input, 1025))
	} else {
		passwordBytes, err = readBoundedFile(*passwordFile, 1024)
	}
	if err != nil || len(passwordBytes) > 1024 {
		clear(passwordBytes)
		return adminidentity.ErrArchiveAuthentication
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(passwordBytes), "\n"), "\r")
	clear(passwordBytes)
	if len(password) != 20 {
		return adminidentity.ErrArchiveAuthentication
	}
	codes, err := adminidentity.DecryptRecoveryArchive(archive, password)
	if err != nil {
		return err
	}
	for _, code := range codes {
		if _, err := fmt.Fprintln(output, code); err != nil {
			return errors.New("write recovery codes")
		}
	}
	return nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(contents)) > limit {
		clear(contents)
		return nil, errors.New("file exceeds limit")
	}
	return contents, nil
}

func serveHTTP(
	ctx context.Context,
	server serverLifecycle,
	address string,
	listen listenFunc,
	gracePeriod time.Duration,
	onListening func(),
) error {
	listener, err := listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen hosted HTTP: %w", err)
	}
	defer listener.Close()
	if onListening != nil {
		onListening()
	}

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve hosted HTTP: %w", err)
	case <-ctx.Done():
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), gracePeriod)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown hosted HTTP: %w", err)
	}

	if err := <-serveErrors; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve hosted HTTP during shutdown: %w", err)
	}
	return nil
}
