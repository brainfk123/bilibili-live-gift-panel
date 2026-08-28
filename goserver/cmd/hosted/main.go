package main

import (
	"context"
	"database/sql"
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
	"sync/atomic"
	"syscall"
	"time"

	"bilibili-live-gift-panel/internal/hosted/adminconsole"
	"bilibili-live-gift-panel/internal/hosted/adminidentity"
	"bilibili-live-gift-panel/internal/hosted/adminsettings"
	"bilibili-live-gift-panel/internal/hosted/app"
	"bilibili-live-gift-panel/internal/hosted/biligateway"
	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/identity/biliqr"
	"bilibili-live-gift-panel/internal/hosted/invitation"
	"bilibili-live-gift-panel/internal/hosted/migration"
	hostedobs "bilibili-live-gift-panel/internal/hosted/obs"
	"bilibili-live-gift-panel/internal/hosted/platform"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
	"bilibili-live-gift-panel/internal/hosted/roomwatcher"
	hostedruntime "bilibili-live-gift-panel/internal/hosted/runtime"
	"bilibili-live-gift-panel/internal/hosted/security"
	"bilibili-live-gift-panel/internal/hosted/store/mysqlstore"
)

const (
	shutdownTimeout   = 30 * time.Second
	hostedUIRoot      = "/srv/gift-panel-hosted/ui"
	hostedLogMaxBytes = 256 * 1024 * 1024
)

type migrationConfigurationBarrierBinder interface {
	SetConfigurationBarrier(migration.ConfigurationBarrier) error
}

func bindMigrationConfigurationBarrier(binder migrationConfigurationBarrierBinder, barrier migration.ConfigurationBarrier) error {
	if binder == nil || barrier == nil {
		return errors.New("invalid migration configuration barrier")
	}
	return binder.SetConfigurationBarrier(barrier)
}

const (
	biliRoomInfoEndpoint    = "https://api.live.bilibili.com/room/v1/Room/room_init"
	biliGiftCatalogEndpoint = "https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftConfig"
	biliDanmakuInfoEndpoint = "https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo"
)

var errInvalidCommand = errors.New("invalid hosted command")

type adminInitializer interface {
	Initialize(context.Context, string) (adminidentity.InitializeResult, error)
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
	logFile, err := openHostedLogWithLimit(config.LogFile, hostedLogMaxBytes, stopSignals)
	if err != nil {
		return errors.New("open hosted application log")
	}
	if logFile != nil {
		previousLogger := slog.Default()
		slog.SetDefault(newHostedLogger(os.Stderr, logFile))
		defer func() {
			slog.SetDefault(previousLogger)
			_ = logFile.Close()
		}()
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
	adminService, err := adminidentity.NewService(adminRepository, keys, sender, adminidentity.ServiceOptions{})
	if err != nil {
		return errors.New("configure administrator identity")
	}
	verifier, err := biliqr.New(biliqr.Config{})
	if err != nil {
		return errors.New("configure Bilibili verification")
	}
	defer verifier.Close()
	return runModeWithCleanup(processContext, os.Args[1:], adminService, os.Stdin, os.Stdout, func(cleanupContext context.Context) {
		adminService.RunHandoffCleanup(cleanupContext, time.Minute)
	}, func() error {
		staticHTTP, err := loadHostedStatic(hostedUIRoot)
		if err != nil {
			return errors.New("load hosted UI")
		}
		resolver, err := identity.NewTrustedProxyClientIPResolver([]string{"127.0.0.1/32", "::1/128"})
		if err != nil {
			return errors.New("configure hosted client address policy")
		}
		limiter := newLocalLimiter(time.Now)
		var runtimeOwner atomic.Pointer[hostedruntime.Manager]
		identityService, err := identity.NewService(identity.NewRepository(store.Database(), adminService), keys, verifier, identity.ServiceOptions{
			OnAccountDisabled: func(accountID int64) {
				if manager := runtimeOwner.Load(); manager != nil {
					manager.AccountDisabled(accountID)
				}
			},
		})
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
		invitationService, err := invitation.NewService(store.Database(), keys, identityService, invitation.ServiceOptions{Administrator: adminService})
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
		configurationRepository := configuration.NewRepository(store.Database())
		configurationService := configuration.NewService(configurationRepository, time.Now)
		configurationHTTP, err := configuration.NewHTTPHandler(configurationService, configuration.HTTPOptions{
			AllowedOrigin: config.AdminAllowedOrigin, CSRFToken: config.AdminCSRFToken,
			Limiter: limiter, ClientIP: resolver, Authenticate: identityHTTP.Authenticate,
		})
		if err != nil {
			return errors.New("configure hosted configuration HTTP")
		}
		migrationRepository := migration.NewRepository(store.Database())
		migrationService := migration.NewService(migrationRepository, time.Now)
		migrationHTTP, err := migration.NewHTTPHandler(migrationService, identityService, migration.HTTPOptions{
			AllowedOrigin: config.AdminAllowedOrigin, CSRFToken: config.AdminCSRFToken,
			Limiter: limiter, ClientIP: resolver, Authenticate: identityHTTP.Authenticate,
		})
		if err != nil {
			return errors.New("configure hosted migration HTTP")
		}
		biliDependencies, err := newProductionBiliGateway(store.Database(), keys, biligateway.NewHTTPUpstream)
		if err != nil {
			return errors.New("configure Bilibili production gateway")
		}
		biliService, err := biligateway.NewService(verifier, biliDependencies.Credentials, adminService, biligateway.ServiceOptions{})
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
		roomSources := roomsource.NewManager(biliDependencies.Gateway, roomsource.Options{})
		runtimePublisher := hostedruntime.NewPublisher()
		runtimeProcessorFactory, err := hostedruntime.NewProcessorFactory(configurationRepository, runtimePublisher, hostedruntime.ProcessorOptions{Alert: func(status hostedruntime.ProcessorStatus) {
			slog.Warn("hosted runtime persistence degraded", "account_id", status.AccountID, "live_session_id", status.LiveSessionID, "buffered", status.Buffered, "rejecting", status.Rejecting, "connection_healthy", status.ConnectionHealthy)
		}})
		if err != nil {
			roomSources.Close()
			return errors.New("configure hosted runtime processor")
		}
		barrierProcessorFactory, err := hostedruntime.NewConfigurationBarrierProcessorFactory(runtimeProcessorFactory)
		if err != nil {
			roomSources.Close()
			return errors.New("configure hosted runtime migration barrier")
		}
		runtimeManager, err := hostedruntime.NewManager(hostedruntime.Dependencies{
			Sessions: hostedruntime.NewSessionRepository(store.Database()), Configuration: configurationRepository,
			Migration: migrationService, RoomSources: roomSources,
		}, hostedruntime.Options{ProcessorFactory: barrierProcessorFactory})
		if err != nil {
			roomSources.Close()
			return errors.New("configure hosted runtime")
		}
		if err := bindMigrationConfigurationBarrier(migrationService, runtimeManager); err != nil {
			roomSources.Close()
			return errors.New("configure hosted migration barrier")
		}
		runtimeOwner.Store(runtimeManager)
		return withProductionRuntimeLifecycle(processContext, shutdownTimeout, runtimeManager, func() { runtimeOwner.Store(nil) }, func(runtimeLifecycle *productionRuntimeLifecycle) error {
			runtimeHTTP, err := hostedruntime.NewHTTPHandler(runtimeManager, hostedruntime.HTTPOptions{
				AllowedOrigin: config.AdminAllowedOrigin, CSRFToken: config.AdminCSRFToken,
				Limiter: limiter, ClientIP: resolver, Authenticate: identityHTTP.Authenticate,
			})
			if err != nil {
				return errors.New("configure hosted runtime HTTP")
			}
			obsService, err := hostedobs.NewService(store.Database(), adminService, hostedobs.ServiceOptions{PublicOrigin: config.AdminAllowedOrigin})
			if err != nil {
				return errors.New("configure hosted OBS")
			}
			if err := migrationHTTP.SetOBSIssuer(func(ctx context.Context, accountID int64) (string, error) {
				issued, issueErr := obsService.IssueForAccount(ctx, accountID)
				return issued.URL, issueErr
			}); err != nil {
				return errors.New("configure migration OBS issuer")
			}
			obsHTTP, err := hostedobs.NewHTTPHandler(obsService, hostedobs.HTTPOptions{
				AllowedOrigin: config.AdminAllowedOrigin, CSRFToken: config.AdminCSRFToken,
				Limiter: limiter, ClientIP: resolver, Runtime: runtimeManager, Publisher: runtimePublisher,
			})
			if err != nil {
				return errors.New("configure hosted OBS HTTP")
			}
			adminHTTP, err := adminidentity.NewHTTPHandler(adminService, adminidentity.HTTPOptions{
				AllowedOrigin: config.AdminAllowedOrigin, CSRFToken: config.AdminCSRFToken,
				Limiter: limiter, ClientIP: resolver,
			})
			if err != nil {
				return errors.New("configure administrator HTTP")
			}
			adminSettingsService, err := adminsettings.NewService(store.Database(), keys, adminService, func(ctx context.Context) string { return biliService.Status(ctx).Health })
			if err != nil {
				return errors.New("configure administrator settings")
			}
			adminSettingsHTTP, err := adminsettings.NewHTTPHandler(adminSettingsService, config.AdminAllowedOrigin, config.AdminCSRFToken)
			if err != nil {
				return errors.New("configure administrator settings HTTP")
			}
			probeInterval, err := app.RoomProbeIntervalFromEnvironment()
			if err != nil {
				return errors.New("configure hosted room probe cadence")
			}
			watcherManager, err := roomwatcher.NewManager(biliDependencies.Gateway, roomwatcher.NewRepository(store.Database()), roomwatcher.Options{})
			if err != nil {
				return errors.New("configure hosted room watcher")
			}
			runtimeLifecycle.TrackWatcher(watcherManager)
			roomRuntime, err := app.StartRoomRuntime(processContext, watcherManager, runtimeManager, app.NewSQLRoomReferenceLoader(store.Database()), app.RoomRuntimeOptions{
				ProbeInterval:     probeInterval,
				FinalDrainTimeout: app.DefaultRoomFinalDrainTimeout,
				OnError: func(error) {
					slog.Warn("hosted room watcher transition retry")
				},
				OnStatus: func(status app.RoomRuntimeStatus) {
					log := slog.Info
					if status.ReadinessAlert {
						log = slog.Warn
					}
					log("hosted room watcher aggregate",
						"watched_rooms", status.WatchedRooms,
						"transition_failures", status.TransitionFailures,
						"grace_transitions", status.GraceTransitions,
						"confirmed_to_ready_samples", status.ReadinessSamples,
						"confirmed_to_ready_within_10_seconds", status.ReadinessWithin10,
						"confirmed_to_ready_within_30_seconds", status.ReadinessWithin30,
						"confirmed_to_ready_over_30_seconds", status.ReadinessOver30,
						"confirmed_to_ready_total", status.ReadinessTotal,
						"confirmed_to_ready_maximum", status.ReadinessMaximum,
						"readiness_alert", status.ReadinessAlert,
						"probe_capacity_per_minute", status.ProbeCapacityPerMinute,
						"probe_available", status.ProbeAvailable,
						"probe_backlog", status.ProbeBacklog,
						"probe_capacity_alert", status.ProbeCapacityAlert,
					)
				},
			})
			if err != nil {
				return errors.New("start hosted room watcher")
			}
			runtimeLifecycle.TrackRoomRuntime(roomRuntime)
			adminConsoleService, err := adminconsole.NewService(store.Database(), config.AdminAllowedOrigin, adminconsole.MutationServices{
				Enable: func(ctx context.Context, token string, accountID int64, reason string) error {
					_, mutationErr := identityService.EnableAccount(ctx, token, accountID, reason)
					return mutationErr
				},
				Disable: func(ctx context.Context, token string, accountID int64, reason string) error {
					_, mutationErr := identityService.DisableAccount(ctx, token, accountID, reason)
					return mutationErr
				},
				SetQuota: func(ctx context.Context, token string, accountID int64, quota uint64, reason string) error {
					_, mutationErr := invitationService.AdjustQuota(ctx, token, accountID, quota, reason)
					return mutationErr
				},
				Room: productionRoomMutation{runtime: runtimeManager, references: roomRuntime},
			})
			if err != nil {
				return errors.New("configure administrator console")
			}
			adminConsoleHTTP, err := adminconsole.NewHTTPHandler(adminConsoleService, adminService)
			if err != nil {
				return errors.New("configure administrator console HTTP")
			}
			metrics := newProductionMetricsSnapshot(roomRuntime, biliDependencies.Gateway)
			handler := composeHostedHTTPWithRuntimeOBSAndStatic(store, metrics, identityHTTP, adminHTTP, invitationHTTP, configurationHTTP, migrationHTTP, biliServiceHTTP, runtimeHTTP, obsHTTP, adminConsoleHTTP, adminSettingsHTTP, staticHTTP, config.AdminCSRFToken)
			server := newHTTPServerWithContext(processContext, config.ListenAddr, retainBiliGateway(handler, biliDependencies.Gateway))
			serveErr := serveHTTPWithRuntime(
				processContext,
				server,
				config.ListenAddr,
				net.Listen,
				shutdownTimeout,
				func() { slog.Info("hosted service listening", "address", config.ListenAddr) },
				runtimeLifecycle.Shutdown,
			)
			if serveErr != nil {
				return serveErr
			}
			return nil
		})
	})
}

var errHostedLogCapacity = errors.New("hosted application log capacity reached")

type hostedLogFile struct {
	file       hostedLogBackend
	maxBytes   int64
	onCapacity func()
	once       sync.Once
	mu         sync.Mutex
}

type hostedLogBackend interface {
	io.Writer
	Stat() (os.FileInfo, error)
	Close() error
}

func (file *hostedLogFile) signalFailure() {
	file.once.Do(func() {
		if file.onCapacity != nil {
			file.onCapacity()
		}
	})
}

func openHostedLog(path string) (*hostedLogFile, error) {
	return openHostedLogWithLimit(path, hostedLogMaxBytes, nil)
}

func openHostedLogWithLimit(path string, maxBytes int64, onCapacity func()) (*hostedLogFile, error) {
	if path == "" {
		return nil, nil
	}
	if maxBytes <= 0 {
		return nil, errors.New("hosted log capacity is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("hosted log path is not a regular file")
	}
	if info.Size() >= maxBytes {
		return nil, errHostedLogCapacity
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil, err
	}
	return &hostedLogFile{file: file, maxBytes: maxBytes, onCapacity: onCapacity}, nil
}

func (file *hostedLogFile) Write(payload []byte) (int, error) {
	file.mu.Lock()
	defer file.mu.Unlock()
	info, err := file.file.Stat()
	if err != nil {
		file.signalFailure()
		return 0, err
	}
	if int64(len(payload)) > file.maxBytes-info.Size() {
		file.signalFailure()
		return 0, errHostedLogCapacity
	}
	written, err := file.file.Write(payload)
	if err != nil {
		file.signalFailure()
		return written, err
	}
	if written != len(payload) {
		file.signalFailure()
		return written, io.ErrShortWrite
	}
	return written, nil
}

func (file *hostedLogFile) Close() error {
	file.mu.Lock()
	defer file.mu.Unlock()
	return file.file.Close()
}

func newHostedLogger(stderr, file io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.MultiWriter(file, stderr), nil))
}

func shutdownAndJoinRuntime(ctx context.Context, shutdown, wait func(context.Context) error) error {
	shutdownErr := shutdown(ctx)
	if !errors.Is(shutdownErr, context.Canceled) && !errors.Is(shutdownErr, context.DeadlineExceeded) {
		return shutdownErr
	}
	return errors.Join(shutdownErr, wait(ctx))
}

type productionManagedRuntime interface {
	Shutdown(context.Context) error
	Wait(context.Context) error
}

type productionStandaloneWatcher interface {
	Close()
	Wait(context.Context) error
}

// productionRuntimeLifecycle is the one post-Manager ownership module. Before
// RoomRuntime starts it owns the standalone watcher; afterwards RoomRuntime
// owns that watcher. Shutdown always quiesces those goroutines before runtime
// closes RoomSources, and only the outer guard clears runtimeOwner.
type productionRuntimeLifecycle struct {
	runtime     productionManagedRuntime
	watcher     productionStandaloneWatcher
	room        productionManagedRuntime
	clearOwner  func()
	mu          sync.Mutex
	startOnce   sync.Once
	done        chan struct{}
	shutdownErr error
}

func (lifecycle *productionRuntimeLifecycle) TrackWatcher(watcher productionStandaloneWatcher) {
	lifecycle.mu.Lock()
	lifecycle.watcher = watcher
	lifecycle.mu.Unlock()
}

func (lifecycle *productionRuntimeLifecycle) TrackRoomRuntime(room productionManagedRuntime) {
	lifecycle.mu.Lock()
	lifecycle.room = room
	lifecycle.watcher = nil
	lifecycle.mu.Unlock()
}

func (lifecycle *productionRuntimeLifecycle) Shutdown(ctx context.Context) error {
	if lifecycle == nil || ctx == nil || lifecycle.runtime == nil {
		return errors.New("invalid production runtime lifecycle")
	}
	lifecycle.startOnce.Do(func() { go lifecycle.shutdownSequence() })
	select {
	case <-lifecycle.done:
		lifecycle.mu.Lock()
		err := lifecycle.shutdownErr
		lifecycle.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (lifecycle *productionRuntimeLifecycle) shutdownSequence() {
	lifecycle.mu.Lock()
	room, watcher, runtime, clearOwner := lifecycle.room, lifecycle.watcher, lifecycle.runtime, lifecycle.clearOwner
	lifecycle.mu.Unlock()
	var roomErr error
	if room != nil {
		roomErr = errors.Join(room.Shutdown(context.Background()), room.Wait(context.Background()))
	} else if watcher != nil {
		watcher.Close()
		roomErr = watcher.Wait(context.Background())
	}
	runtimeErr := errors.Join(runtime.Shutdown(context.Background()), runtime.Wait(context.Background()))
	err := errors.Join(roomErr, runtimeErr)
	if clearOwner != nil {
		clearOwner()
	}
	lifecycle.mu.Lock()
	lifecycle.shutdownErr = err
	lifecycle.mu.Unlock()
	close(lifecycle.done)
}

func (lifecycle *productionRuntimeLifecycle) Done() <-chan struct{} {
	if lifecycle == nil {
		return nil
	}
	return lifecycle.done
}

func withProductionRuntimeLifecycle(ctx context.Context, timeout time.Duration, runtime productionManagedRuntime, clearOwner func(), initialize func(*productionRuntimeLifecycle) error) (resultErr error) {
	if ctx == nil || timeout <= 0 || runtime == nil || clearOwner == nil || initialize == nil {
		return errors.New("invalid production runtime composition")
	}
	lifecycle := &productionRuntimeLifecycle{runtime: runtime, clearOwner: clearOwner, done: make(chan struct{})}
	defer func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), timeout)
		cleanupErr := lifecycle.Shutdown(cleanupContext)
		cancelCleanup()
		resultErr = errors.Join(resultErr, cleanupErr)
	}()
	return initialize(lifecycle)
}

type hostedHealthChecker interface {
	Health(context.Context) error
}

type biliUpstreamFactory func(biligateway.HTTPUpstreamOptions) (*biligateway.HTTPUpstream, error)

type productionBiliGateway struct {
	Credentials *biligateway.CredentialStore
	Gateway     *biligateway.ControlledGateway
}

type runtimeRoomSetter interface {
	MutateRoom(context.Context, int64, string) (hostedruntime.RoomMutationResult, error)
	AdvanceRoomMutation(context.Context, hostedruntime.RoomMutationID, hostedruntime.RoomMutationPhase, hostedruntime.RoomMutationPhase) (hostedruntime.RoomMutationResult, error)
}

type roomReferenceRefresher interface {
	RefreshReferences(context.Context) error
}

// productionRoomMutation is the single administrator room-change adapter:
// runtime owns canonicalization, ownership fencing and persistence; only a
// successful runtime mutation may publish the new durable reference snapshot.
type productionRoomMutation struct {
	runtime    runtimeRoomSetter
	references roomReferenceRefresher
}

func (mutation productionRoomMutation) SetRoom(ctx context.Context, accountID int64, roomID string) (adminconsole.RoomMutationResult, error) {
	if mutation.runtime == nil || mutation.references == nil {
		return adminconsole.RoomMutationResult{}, errors.New("hosted room mutation unavailable")
	}
	result, err := mutation.runtime.MutateRoom(ctx, accountID, roomID)
	if err != nil {
		if errors.Is(err, hostedruntime.ErrAccountNotFound) {
			return adminconsole.RoomMutationResult{}, adminconsole.ErrNotFound
		}
		return adminconsole.RoomMutationResult{}, err
	}
	if result.Phase == hostedruntime.RoomMutationTargetPersisted {
		if err := mutation.references.RefreshReferences(ctx); err != nil {
			return adminconsole.RoomMutationResult{}, err
		}
		result, err = mutation.runtime.AdvanceRoomMutation(ctx, result.MutationID, hostedruntime.RoomMutationTargetPersisted, hostedruntime.RoomMutationReferencesSynced)
		if err != nil {
			return adminconsole.RoomMutationResult{}, err
		}
	} else if result.Phase != hostedruntime.RoomMutationReferencesSynced && result.Phase != hostedruntime.RoomMutationAudited {
		return adminconsole.RoomMutationResult{}, errors.New("hosted room mutation receipt unavailable")
	}
	return adminconsole.RoomMutationResult{MutationID: [16]byte(result.MutationID), AccountID: result.AccountID, DesiredCanonical: result.DesiredCanonical, OldCanonical: result.OldCanonical, NewCanonical: result.NewCanonical, Phase: adminconsole.RoomMutationPhase(result.Phase)}, nil
}

func newProductionBiliGateway(database *sql.DB, keys security.Keyring, newUpstream biliUpstreamFactory) (productionBiliGateway, error) {
	if database == nil || newUpstream == nil {
		return productionBiliGateway{}, errors.New("invalid Bilibili production dependencies")
	}
	upstream, err := newUpstream(biligateway.HTTPUpstreamOptions{
		RoomInfoEndpoint:    biliRoomInfoEndpoint,
		GiftCatalogEndpoint: biliGiftCatalogEndpoint,
		DanmakuInfoEndpoint: biliDanmakuInfoEndpoint,
	})
	if err != nil {
		return productionBiliGateway{}, fmt.Errorf("construct Bilibili HTTP upstream: %w", err)
	}
	if upstream == nil {
		return productionBiliGateway{}, errors.New("construct Bilibili HTTP upstream")
	}
	credentials := biligateway.NewCredentialStore(database, keys)
	gateway := biligateway.NewControlledGateway(upstream, credentials, biligateway.GatewayOptions{Now: time.Now})
	if gateway == nil {
		return productionBiliGateway{}, errors.New("construct controlled Bilibili gateway")
	}
	return productionBiliGateway{Credentials: credentials, Gateway: gateway}, nil
}

// biliGatewayOwner keeps the production gateway reachable for exactly the
// HTTP server lifetime until the runtime manager becomes its direct consumer.
type biliGatewayOwner struct {
	http.Handler
	Gateway biligateway.Gateway
}

func retainBiliGateway(handler http.Handler, gateway biligateway.Gateway) http.Handler {
	return &biliGatewayOwner{Handler: handler, Gateway: gateway}
}

func composeHostedHTTP(database hostedHealthChecker, auth, admin, invitations, configurationHTTP, migrationHTTP, biliServiceHTTP http.Handler, csrfToken string) http.Handler {
	return composeHostedHTTPWithRuntime(database, auth, admin, invitations, configurationHTTP, migrationHTTP, biliServiceHTTP, nil, csrfToken)
}

func composeHostedHTTPWithRuntime(database hostedHealthChecker, auth, admin, invitations, configurationHTTP, migrationHTTP, biliServiceHTTP, runtimeHTTP http.Handler, csrfToken string) http.Handler {
	return composeHostedHTTPWithRuntimeAndOBS(database, auth, admin, invitations, configurationHTTP, migrationHTTP, biliServiceHTTP, runtimeHTTP, nil, csrfToken)
}

func composeHostedHTTPWithRuntimeAndOBS(database hostedHealthChecker, auth, admin, invitations, configurationHTTP, migrationHTTP, biliServiceHTTP, runtimeHTTP, obsHTTP http.Handler, csrfToken string) http.Handler {
	return composeHostedHTTPWithRuntimeOBSAndStatic(database, nil, auth, admin, invitations, configurationHTTP, migrationHTTP, biliServiceHTTP, runtimeHTTP, obsHTTP, nil, nil, nil, csrfToken)
}

func composeHostedHTTPWithRuntimeOBSAndStatic(database hostedHealthChecker, metrics app.MetricsSnapshotFunc, auth, admin, invitations, configurationHTTP, migrationHTTP, biliServiceHTTP, runtimeHTTP, obsHTTP, adminConsoleHTTP, adminSettingsHTTP, staticHTTP http.Handler, csrfToken string) http.Handler {
	return app.New(app.Dependencies{DB: database, Metrics: metrics, Auth: auth, Admin: admin, AdminConsole: adminConsoleHTTP, AdminSettings: adminSettingsHTTP, Invitation: invitations, Configuration: configurationHTTP, Migration: migrationHTTP, BiliService: biliServiceHTTP, Runtime: runtimeHTTP, OBS: obsHTTP, Static: staticHTTP, CSRFToken: csrfToken})
}

type roomMetricsSource interface {
	Status() app.RoomRuntimeStatus
}

type biliMetricsSource interface {
	Status() biligateway.Status
}

func newProductionMetricsSnapshot(room roomMetricsSource, bili biliMetricsSource) app.MetricsSnapshotFunc {
	if room == nil || bili == nil {
		return nil
	}
	return func() app.MetricsSnapshot {
		return app.MetricsSnapshot{
			BilibiliBreakerOpen: bili.Status().EgressOpen,
			Room:                room.Status(),
		}
	}
}

func loadHostedStatic(root string) (http.Handler, error) {
	return app.NewStaticHandler(root)
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
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
	}
}

func newHTTPServerWithContext(processContext context.Context, address string, handler http.Handler) *http.Server {
	if processContext == nil {
		processContext = context.Background()
	}
	server := newHTTPServer(address, handler)
	server.BaseContext = func(net.Listener) context.Context { return processContext }
	return server
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
	email := flags.String("email", "", "administrator recovery email")
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *email == "" {
		return errInvalidCommand
	}
	result, err := initializer.Initialize(ctx, *email)
	if err != nil {
		return err
	}
	if result.TOTPURI == "" || len(result.RecoveryPassword) != 20 || result.HandoffToken == "" {
		return adminidentity.ErrUnavailable
	}
	if _, err := fmt.Fprintf(output, "TOTP URI: %s\nRecovery package password: %s\nConfirmation token: %s\n", result.TOTPURI, result.RecoveryPassword, result.HandoffToken); err != nil {
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

func serveHTTPWithRuntime(
	ctx context.Context,
	server serverLifecycle,
	address string,
	listen listenFunc,
	gracePeriod time.Duration,
	onListening func(),
	shutdownRuntime func(context.Context) error,
) error {
	if shutdownRuntime == nil {
		return serveHTTP(ctx, server, address, listen, gracePeriod, onListening)
	}
	shutdownRequested := make(chan struct{})
	var requestOnce sync.Once
	requestShutdown := func() { requestOnce.Do(func() { close(shutdownRequested) }) }
	runtimeErrors := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
		case <-shutdownRequested:
		}
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), gracePeriod)
		defer cancelShutdown()
		runtimeErrors <- shutdownRuntime(shutdownContext)
	}()

	serveErr := serveHTTP(ctx, server, address, listen, gracePeriod, onListening)
	requestShutdown()
	runtimeErr := <-runtimeErrors
	if runtimeErr != nil {
		runtimeErr = fmt.Errorf("shutdown hosted runtime: %w", runtimeErr)
	}
	return errors.Join(serveErr, runtimeErr)
}
