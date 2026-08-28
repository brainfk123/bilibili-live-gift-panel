package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/hosted/roomwatcher"
)

type healthChecker interface {
	Health(context.Context) error
}

var (
	ErrRoomRuntimeInvalid     = errors.New("hosted room runtime: invalid input")
	ErrRoomRuntimeUnavailable = errors.New("hosted room runtime: unavailable")
)

const (
	DefaultRoomProbeInterval     = 30 * time.Second
	DefaultRoomFinalDrainTimeout = 10 * time.Second
	minimumRoomProbeInterval     = 10 * time.Second
	maximumRoomProbeInterval     = 5 * time.Minute
	defaultRoomReplayLimit       = roomwatcher.MaxReplayLimit
	defaultRoomRetryInterval     = 5 * time.Second
)

// RoomWatcher is the narrow production composition surface. Events is a
// bounded wake-up stream; ReplayEvents is the authoritative durable stream.
type RoomWatcher interface {
	LoadBootstrap(context.Context) (roomwatcher.Bootstrap, error)
	RestoreBootstrap(roomwatcher.Bootstrap) error
	ReplayEvents(context.Context, uint64, int) ([]roomwatcher.Event, error)
	SetReferences(context.Context, []roomwatcher.Reference) error
	Poll(context.Context) error
	Events() <-chan roomwatcher.Event
	Close()
	Wait(context.Context) error
}

type RoomEventRuntime interface {
	BootstrapRoomProjection(context.Context, roomwatcher.Bootstrap) error
	ApplyRoomEvent(context.Context, roomwatcher.Event) error
}

type RoomReferenceLoader interface {
	LoadEnabledRoomReferences(context.Context) ([]roomwatcher.Reference, error)
}

type SQLRoomReferenceLoader struct{ db *sql.DB }

func NewSQLRoomReferenceLoader(database *sql.DB) *SQLRoomReferenceLoader {
	return &SQLRoomReferenceLoader{db: database}
}

// LoadEnabledRoomReferences reads the complete current product projection.
// Invalid database rows fail closed before roomwatcher changes its snapshot.
func (loader *SQLRoomReferenceLoader) LoadEnabledRoomReferences(ctx context.Context) ([]roomwatcher.Reference, error) {
	if loader == nil || loader.db == nil || ctx == nil {
		return nil, ErrRoomRuntimeInvalid
	}
	rows, err := loader.db.QueryContext(ctx, "SELECT a.id, r.room_id FROM streamer_accounts AS a JOIN account_runtime_rooms AS r ON r.account_id = a.id WHERE a.disabled_at IS NULL ORDER BY r.room_id, a.id")
	if err != nil {
		return nil, ErrRoomRuntimeUnavailable
	}
	defer rows.Close()
	references := make([]roomwatcher.Reference, 0)
	for rows.Next() {
		var reference roomwatcher.Reference
		if err := rows.Scan(&reference.AccountID, &reference.RoomID); err != nil || reference.AccountID <= 0 || !canonicalRoomReference(reference.RoomID) {
			return nil, ErrRoomRuntimeUnavailable
		}
		references = append(references, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrRoomRuntimeUnavailable
	}
	return references, nil
}

func canonicalRoomReference(roomID string) bool {
	numeric, err := strconv.ParseUint(roomID, 10, 64)
	return err == nil && numeric > 0 && strconv.FormatUint(numeric, 10) == roomID
}

// RoomProbeIntervalFromEnvironment keeps cadence configurable without adding
// the pilot value to the stable product configuration contract. The 30-second
// default is intentionally conservative and is not claimed as a real-room
// validated final value.
func RoomProbeIntervalFromEnvironment() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("HOSTED_ROOM_PROBE_INTERVAL"))
	if raw == "" {
		return DefaultRoomProbeInterval, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval < minimumRoomProbeInterval || interval > maximumRoomProbeInterval {
		return 0, ErrRoomRuntimeInvalid
	}
	return interval, nil
}

type RoomRuntimeStatus struct {
	WatchedRooms           int           `json:"watchedRooms"`
	TransitionFailures     uint64        `json:"transitionFailures"`
	GraceTransitions       uint64        `json:"graceTransitions"`
	ReadinessSamples       uint64        `json:"confirmedToReadySamples"`
	ReadinessWithin10      uint64        `json:"confirmedToReadyWithin10Seconds"`
	ReadinessWithin30      uint64        `json:"confirmedToReadyWithin30Seconds"`
	ReadinessOver30        uint64        `json:"confirmedToReadyOver30Seconds"`
	ReadinessTotal         time.Duration `json:"confirmedToReadyTotal"`
	ReadinessMaximum       time.Duration `json:"confirmedToReadyMaximum"`
	ReadinessAlert         bool          `json:"confirmedToReadyAlert"`
	ProbeCapacityPerMinute int           `json:"probeCapacityPerMinute"`
	ProbeAvailable         int           `json:"probeAvailable"`
	ProbeBacklog           int           `json:"probeBacklog"`
	ProbeCapacityAlert     bool          `json:"probeCapacityAlert"`
}

type roomRuntimeTicker interface {
	C() <-chan time.Time
	Stop()
}

type systemRoomRuntimeTicker struct{ ticker *time.Ticker }

func (ticker *systemRoomRuntimeTicker) C() <-chan time.Time { return ticker.ticker.C }
func (ticker *systemRoomRuntimeTicker) Stop()               { ticker.ticker.Stop() }

type RoomRuntimeOptions struct {
	ProbeInterval     time.Duration
	ReplayLimit       int
	FinalDrainTimeout time.Duration
	Now               func() time.Time
	OnStatus          func(RoomRuntimeStatus)
	OnError           func(error)

	newTicker  func(time.Duration) roomRuntimeTicker
	retryAfter func(time.Duration) <-chan time.Time
}

// RoomRuntime owns production composition only: durable bootstrap/replay,
// reference refresh, watcher scheduling, and aggregate readiness metrics.
type RoomRuntime struct {
	watcher    RoomWatcher
	runtime    RoomEventRuntime
	references RoomReferenceLoader
	options    RoomRuntimeOptions

	mu     sync.Mutex
	cursor uint64
	status RoomRuntimeStatus

	referenceMu sync.Mutex

	pollCancel    context.CancelFunc
	consumeCancel context.CancelFunc
	pollDone      chan struct{}
	consumeDone   chan struct{}
	done          chan struct{}
	shutdownOnce  sync.Once
	shutdownErr   error
}

func StartRoomRuntime(ctx context.Context, watcher RoomWatcher, runtime RoomEventRuntime, references RoomReferenceLoader, options RoomRuntimeOptions) (*RoomRuntime, error) {
	if ctx == nil || watcher == nil || runtime == nil || references == nil {
		return nil, ErrRoomRuntimeInvalid
	}
	if options.ProbeInterval == 0 {
		options.ProbeInterval = DefaultRoomProbeInterval
	}
	if options.ProbeInterval < minimumRoomProbeInterval || options.ProbeInterval > maximumRoomProbeInterval {
		return nil, ErrRoomRuntimeInvalid
	}
	if options.ReplayLimit == 0 {
		options.ReplayLimit = defaultRoomReplayLimit
	}
	if options.ReplayLimit <= 0 || options.ReplayLimit > roomwatcher.MaxReplayLimit {
		return nil, ErrRoomRuntimeInvalid
	}
	if options.FinalDrainTimeout == 0 {
		options.FinalDrainTimeout = DefaultRoomFinalDrainTimeout
	}
	if options.FinalDrainTimeout < 0 {
		return nil, ErrRoomRuntimeInvalid
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.newTicker == nil {
		options.newTicker = func(interval time.Duration) roomRuntimeTicker {
			return &systemRoomRuntimeTicker{ticker: time.NewTicker(interval)}
		}
	}
	if options.retryAfter == nil {
		options.retryAfter = func(delay time.Duration) <-chan time.Time { return time.After(delay) }
	}
	composition := &RoomRuntime{
		watcher: watcher, runtime: runtime, references: references, options: options,
		pollDone: make(chan struct{}), consumeDone: make(chan struct{}), done: make(chan struct{}),
	}
	bootstrap, err := watcher.LoadBootstrap(ctx)
	if err != nil {
		return nil, ErrRoomRuntimeUnavailable
	}
	if err := runtime.BootstrapRoomProjection(ctx, bootstrap); err != nil {
		return nil, ErrRoomRuntimeUnavailable
	}
	if err := watcher.RestoreBootstrap(bootstrap); err != nil {
		return nil, ErrRoomRuntimeUnavailable
	}
	composition.cursor = bootstrap.Cursor
	if err := composition.replay(ctx); err != nil {
		return nil, err
	}
	// Restored rooms need one immediate observation. Fresh rooms are probed by
	// SetReferences below, so polling first avoids probing an added shared room
	// twice during the same startup cadence.
	if len(bootstrap.Rooms) != 0 {
		if err := watcher.Poll(ctx); err != nil {
			composition.recordFailure(err)
		}
		composition.recordProbeCapacity()
	}
	currentReferences, err := references.LoadEnabledRoomReferences(ctx)
	if err != nil {
		return nil, ErrRoomRuntimeUnavailable
	}
	if err := watcher.SetReferences(ctx, currentReferences); err != nil {
		composition.recordFailure(err)
	} else {
		composition.setWatchedRooms(currentReferences)
		composition.recordProbeCapacity()
	}
	pollContext, pollCancel := context.WithCancel(context.Background())
	consumeContext, consumeCancel := context.WithCancel(context.Background())
	composition.pollCancel = pollCancel
	composition.consumeCancel = consumeCancel
	go composition.pollLoop(pollContext)
	go composition.consumeLoop(consumeContext)
	return composition, nil
}

func (runtime *RoomRuntime) replay(ctx context.Context) error {
	for {
		runtime.mu.Lock()
		cursor := runtime.cursor
		runtime.mu.Unlock()
		events, err := runtime.watcher.ReplayEvents(ctx, cursor, runtime.options.ReplayLimit)
		if err != nil {
			return ErrRoomRuntimeUnavailable
		}
		for _, event := range events {
			if event.Sequence <= cursor {
				return ErrRoomRuntimeUnavailable
			}
			if err := runtime.runtime.ApplyRoomEvent(ctx, event); err != nil {
				return ErrRoomRuntimeUnavailable
			}
			cursor = event.Sequence
			runtime.recordApplied(event, cursor)
		}
		if len(events) < runtime.options.ReplayLimit {
			return nil
		}
	}
}

func (runtime *RoomRuntime) pollLoop(ctx context.Context) {
	defer close(runtime.pollDone)
	ticker := runtime.options.newTicker(runtime.options.ProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
		}
		if err := runtime.watcher.Poll(ctx); err != nil && ctx.Err() == nil {
			runtime.recordFailure(err)
		}
		runtime.recordProbeCapacity()
		if ctx.Err() != nil {
			return
		}
		_ = runtime.RefreshReferences(ctx)
	}
}

// RefreshReferences immediately reloads the complete enabled-account room
// projection and durably syncs it through roomwatcher. The lock covers both
// the database read and SetReferences so a cadence refresh cannot overwrite a
// newer administrator-triggered snapshot with an older in-flight read.
func (runtime *RoomRuntime) RefreshReferences(ctx context.Context) error {
	if runtime == nil || ctx == nil {
		return ErrRoomRuntimeInvalid
	}
	runtime.referenceMu.Lock()
	defer runtime.referenceMu.Unlock()
	references, err := runtime.references.LoadEnabledRoomReferences(ctx)
	if err != nil {
		runtime.recordFailure(err)
		return ErrRoomRuntimeUnavailable
	}
	if err := runtime.watcher.SetReferences(ctx, references); err != nil {
		runtime.recordFailure(err)
		return ErrRoomRuntimeUnavailable
	}
	runtime.setWatchedRooms(references)
	runtime.recordProbeCapacity()
	return nil
}

func (runtime *RoomRuntime) consumeLoop(ctx context.Context) {
	defer close(runtime.consumeDone)
	events := runtime.watcher.Events()
	closed := false
	for {
		if !closed {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				closed = !ok
			}
		}
		for {
			err := runtime.replay(ctx)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				return
			}
			runtime.recordFailure(err)
			select {
			case <-ctx.Done():
				return
			case <-runtime.options.retryAfter(defaultRoomRetryInterval):
			}
		}
		if closed {
			return
		}
	}
}

func (runtime *RoomRuntime) recordApplied(event roomwatcher.Event, cursor uint64) {
	runtime.mu.Lock()
	runtime.cursor = cursor
	if transition := event.RoomStateChanged; transition != nil {
		if transition.To == roomwatcher.StateGrace {
			runtime.status.GraceTransitions++
		}
		if transition.To == roomwatcher.StateLive {
			duration := runtime.options.Now().Sub(transition.ConfirmedAt)
			if duration < 0 {
				duration = 0
			}
			runtime.status.ReadinessSamples++
			runtime.status.ReadinessTotal += duration
			if duration > runtime.status.ReadinessMaximum {
				runtime.status.ReadinessMaximum = duration
			}
			if duration <= 10*time.Second {
				runtime.status.ReadinessWithin10++
			}
			if duration <= 30*time.Second {
				runtime.status.ReadinessWithin30++
			} else {
				runtime.status.ReadinessOver30++
				runtime.status.ReadinessAlert = true
			}
		}
	}
	status, onStatus := runtime.status, runtime.options.OnStatus
	runtime.mu.Unlock()
	if onStatus != nil {
		onStatus(status)
	}
}

func (runtime *RoomRuntime) recordFailure(err error) {
	runtime.mu.Lock()
	runtime.status.TransitionFailures++
	status, onStatus, onError := runtime.status, runtime.options.OnStatus, runtime.options.OnError
	runtime.mu.Unlock()
	if onError != nil {
		onError(ErrRoomRuntimeUnavailable)
	}
	if onStatus != nil {
		onStatus(status)
	}
	_ = err
}

func (runtime *RoomRuntime) setWatchedRooms(references []roomwatcher.Reference) {
	rooms := make(map[string]struct{}, len(references))
	for _, reference := range references {
		rooms[reference.RoomID] = struct{}{}
	}
	runtime.mu.Lock()
	changed := runtime.status.WatchedRooms != len(rooms)
	runtime.status.WatchedRooms = len(rooms)
	status, onStatus := runtime.status, runtime.options.OnStatus
	runtime.mu.Unlock()
	if changed && onStatus != nil {
		onStatus(status)
	}
}

type roomProbeCapacityReporter interface {
	ProbeCapacity() roomwatcher.ProbeCapacityStatus
}

func (runtime *RoomRuntime) recordProbeCapacity() {
	reporter, ok := runtime.watcher.(roomProbeCapacityReporter)
	if !ok {
		return
	}
	capacity := reporter.ProbeCapacity()
	runtime.mu.Lock()
	runtime.status.ProbeCapacityPerMinute = capacity.CapacityPerMinute
	runtime.status.ProbeAvailable = capacity.Available
	runtime.status.ProbeBacklog = capacity.Backlog
	requiredSweep := time.Duration(0)
	if capacity.CapacityPerMinute > 0 {
		requiredSweep = time.Duration(runtime.status.WatchedRooms) * time.Minute / time.Duration(capacity.CapacityPerMinute)
	}
	runtime.status.ProbeCapacityAlert = capacity.Backlog > 0 || runtime.status.WatchedRooms > 0 && (capacity.CapacityPerMinute <= 0 || requiredSweep > runtime.options.ProbeInterval)
	status, onStatus := runtime.status, runtime.options.OnStatus
	runtime.mu.Unlock()
	if onStatus != nil {
		onStatus(status)
	}
}

func (runtime *RoomRuntime) Status() RoomRuntimeStatus {
	if runtime == nil {
		return RoomRuntimeStatus{}
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.status
}

// Shutdown starts one background-owned join. Caller cancellation only stops
// waiting; it never controls whether owned goroutines are allowed to finish.
func (runtime *RoomRuntime) Shutdown(ctx context.Context) error {
	if runtime == nil || ctx == nil {
		return ErrRoomRuntimeInvalid
	}
	runtime.shutdownOnce.Do(func() {
		go runtime.shutdownSequence()
	})
	select {
	case <-runtime.done:
		runtime.mu.Lock()
		err := runtime.shutdownErr
		runtime.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (runtime *RoomRuntime) shutdownSequence() {
	runtime.pollCancel()
	<-runtime.pollDone
	runtime.watcher.Close()
	// Once the producer is closed, no new wake-up can arrive. Cancel the
	// normal retry loop so permanent ReplayEvents/ApplyRoomEvent failures
	// cannot prevent the lifecycle join.
	runtime.consumeCancel()
	<-runtime.consumeDone
	drainContext, cancelDrain := context.WithTimeout(context.Background(), runtime.options.FinalDrainTimeout)
	drainErr := runtime.replay(drainContext)
	cancelDrain()
	if drainErr != nil {
		runtime.recordFailure(drainErr)
	}
	err := errors.Join(drainErr, runtime.watcher.Wait(context.Background()))
	runtime.mu.Lock()
	runtime.shutdownErr = err
	runtime.mu.Unlock()
	close(runtime.done)
}

func (runtime *RoomRuntime) Wait(ctx context.Context) error {
	if runtime == nil || ctx == nil {
		return ErrRoomRuntimeInvalid
	}
	select {
	case <-runtime.done:
		runtime.mu.Lock()
		err := runtime.shutdownErr
		runtime.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Dependencies contains the hosted HTTP application's external services.
type Dependencies struct {
	DB            healthChecker
	Metrics       MetricsSnapshotFunc
	Auth          http.Handler
	Admin         http.Handler
	AdminConsole  http.Handler
	AdminSettings http.Handler
	Invitation    http.Handler
	Configuration http.Handler
	Migration     http.Handler
	BiliService   http.Handler
	Runtime       http.Handler
	OBS           http.Handler
	Static        http.Handler
	CSRFToken     string
}

var obsLandingPublicIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

var obsLandingThemes = map[string]struct{}{
	"minimal": {},
	"glass":   {},
	"rpg":     {},
	"pixel":   {},
	"neon":    {},
	"kawaii":  {},
}

type manifestEntry struct {
	File   string   `json:"file"`
	CSS    []string `json:"css"`
	Assets []string `json:"assets"`
}

type staticContent struct {
	body        []byte
	contentType string
	cache       string
}

type staticHandler struct {
	hosted staticContent
	obs    staticContent
	assets map[string]staticContent
}

// NewStaticHandler validates and preloads the immutable hosted UI bundle. Only
// the two entry pages and files named by Vite's manifest can ever be served.
func NewStaticHandler(root string) (http.Handler, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, errors.New("hosted UI root must be absolute")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("hosted UI root is unavailable")
	}
	read := func(name string) ([]byte, error) {
		fileName := filepath.Join(root, filepath.FromSlash(name))
		info, statErr := os.Lstat(fileName)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("hosted UI file %q is unavailable", name)
		}
		contents, readErr := os.ReadFile(fileName)
		if readErr != nil {
			return nil, fmt.Errorf("read hosted UI file %q", name)
		}
		return contents, nil
	}
	hostedPage, err := read("hosted.html")
	if err != nil {
		return nil, err
	}
	obsPage, err := read("obs.html")
	if err != nil {
		return nil, err
	}
	manifestBytes, err := read(".vite/manifest.json")
	if err != nil {
		return nil, err
	}
	var manifest map[string]manifestEntry
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil || len(manifest) == 0 {
		return nil, errors.New("hosted UI manifest is invalid")
	}
	if _, ok := manifest["hosted.html"]; !ok {
		return nil, errors.New("hosted UI manifest lacks hosted entry")
	}
	if _, ok := manifest["obs.html"]; !ok {
		return nil, errors.New("hosted UI manifest lacks OBS entry")
	}
	assets := make(map[string]staticContent)
	for _, entry := range manifest {
		files := append([]string{entry.File}, entry.CSS...)
		files = append(files, entry.Assets...)
		for _, name := range files {
			if name == "" {
				continue
			}
			if !strings.HasPrefix(name, "assets/") || path.Clean(name) != name || strings.Contains(name, `\`) {
				return nil, errors.New("hosted UI manifest contains an invalid asset path")
			}
			contents, readErr := read(name)
			if readErr != nil {
				return nil, readErr
			}
			contentType := mime.TypeByExtension(filepath.Ext(name))
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			assets["/"+name] = staticContent{body: contents, contentType: contentType, cache: "public, max-age=31536000, immutable"}
		}
	}
	return &staticHandler{
		hosted: staticContent{body: hostedPage, contentType: "text/html; charset=utf-8", cache: "no-store"},
		obs:    staticContent{body: obsPage, contentType: "text/html; charset=utf-8", cache: "no-store"},
		assets: assets,
	}, nil
}

func (handler *staticHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if handler == nil || request == nil {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var content staticContent
	var ok bool
	switch {
	case request.URL.Path == "/" || request.URL.Path == "/hosted.html":
		if request.URL.RawQuery == "" {
			content, ok = handler.hosted, true
		}
	case strings.HasPrefix(request.URL.Path, "/obs/") && obsLandingPublicIDPattern.MatchString(strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/obs/"), "/")):
		if validOBSThemeQuery(request.URL.RawQuery) {
			content, ok = handler.obs, true
		}
	default:
		if request.URL.RawQuery == "" {
			content, ok = handler.assets[request.URL.Path]
		}
	}
	if !ok {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", content.contentType)
	response.Header().Set("Cache-Control", content.cache)
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method == http.MethodHead {
		response.WriteHeader(http.StatusOK)
		return
	}
	_, _ = response.Write(content.body)
}

func validOBSThemeQuery(rawQuery string) bool {
	if rawQuery == "" {
		return true
	}
	for theme := range obsLandingThemes {
		if rawQuery == "theme="+theme {
			return true
		}
	}
	return false
}

// New builds the hosted HTTP handler.
func New(dependencies Dependencies) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		status := "ok"
		statusCode := http.StatusOK
		if dependencies.DB == nil || dependencies.DB.Health(request.Context()) != nil {
			status = "unavailable"
			statusCode = http.StatusServiceUnavailable
		}
		response.WriteHeader(statusCode)
		_ = json.NewEncoder(response).Encode(struct {
			Status string `json:"status"`
		}{Status: status})
	})
	if dependencies.Metrics != nil {
		mux.Handle("/internal/metrics", internalMetricsHandler(dependencies.Metrics))
	}
	mux.HandleFunc("/api/bootstrap", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "application/json")
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			response.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(response).Encode(struct {
				Error string `json:"error"`
			}{Error: "request_rejected"})
			return
		}
		if request.URL.RawQuery != "" {
			response.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(response).Encode(struct {
				Error string `json:"error"`
			}{Error: "invalid_request"})
			return
		}
		_ = json.NewEncoder(response).Encode(struct {
			CSRFToken string `json:"csrfToken"`
		}{CSRFToken: dependencies.CSRFToken})
	})
	// Keep these exact method-routes ahead of broad authentication and admin
	// prefixes so requests cannot fall through to a less specific handler.
	if dependencies.Configuration != nil {
		mux.Handle("GET /api/configuration", dependencies.Configuration)
		mux.Handle("PUT /api/configuration/definition", dependencies.Configuration)
		mux.Handle("PUT /api/configuration/state", dependencies.Configuration)
		mux.Handle("PUT /api/configuration/room-suggestion", dependencies.Configuration)
	}
	if dependencies.Migration != nil {
		mux.Handle("POST /api/migrations/preview", dependencies.Migration)
		mux.Handle("GET /api/migrations", dependencies.Migration)
		mux.Handle("PUT /api/migrations/{id}/selection", dependencies.Migration)
		mux.Handle("POST /api/migrations/{id}/apply", dependencies.Migration)
		mux.Handle("DELETE /api/migrations/{id}", dependencies.Migration)
		mux.Handle("POST /api/migrations/{id}/rollback", dependencies.Migration)
		mux.Handle("POST /api/migrations/{id}/obs-links", dependencies.Migration)
		mux.Handle("GET /api/migrations/{id}", dependencies.Migration)
	}
	if dependencies.BiliService != nil {
		// Own each complete path for every method. The Bili service handler
		// returns 405 for unsupported methods instead of letting them fall
		// through to the broader administrator router.
		mux.Handle("/api/admin/bili-service/challenge", dependencies.BiliService)
		mux.Handle("/api/admin/bili-service/challenge/{id}", dependencies.BiliService)
		mux.Handle("/api/admin/bili-service/replace", dependencies.BiliService)
		mux.Handle("/api/admin/bili-service/status", dependencies.BiliService)
		mux.Handle("/api/admin/bili-service/check", dependencies.BiliService)
	}
	if dependencies.Runtime != nil {
		// Own each complete runtime path so unsupported methods cannot fall
		// through to an unrelated broad handler. There is deliberately no
		// start or stop path.
		mux.Handle("/api/runtime/room", dependencies.Runtime)
		mux.Handle("/api/runtime/events", dependencies.Runtime)
		mux.Handle("/api/runtime/status", dependencies.Runtime)
	}
	if dependencies.OBS != nil {
		// OBS owns every method on its credential, exchange, and event paths so
		// mutations cannot fall through to broader account/admin handlers.
		mux.Handle("/api/admin/accounts/{id}/obs-credential", dependencies.OBS)
		mux.Handle("/obs/{publicID}/exchange", dependencies.OBS)
		mux.Handle("/obs/{publicID}/events", dependencies.OBS)
	}
	if dependencies.AdminConsole != nil {
		// Administrator projections own these exact read paths before the
		// legacy broad account mutation handler.
		mux.Handle("GET /api/admin/overview", dependencies.AdminConsole)
		mux.Handle("GET /api/admin/accounts", dependencies.AdminConsole)
		mux.Handle("GET /api/admin/accounts/{id}", dependencies.AdminConsole)
		mux.Handle("POST /api/admin/accounts/batch", dependencies.AdminConsole)
		mux.Handle("PUT /api/admin/accounts/{id}/room", dependencies.AdminConsole)
	}
	if dependencies.AdminSettings != nil {
		mux.Handle("GET /api/admin/settings", dependencies.AdminSettings)
		// Own every method on the three session-inventory paths. The settings
		// handler returns 405 for unsupported methods instead of allowing a
		// request to fall through to broad authentication or administrator
		// prefixes.
		mux.Handle("/api/admin/sessions", dependencies.AdminSettings)
		mux.Handle("/api/admin/sessions/{publicId}", dependencies.AdminSettings)
		mux.Handle("/api/admin/login-events", dependencies.AdminSettings)
		mux.Handle("POST /api/admin/sessions/revoke-others", dependencies.AdminSettings)
		mux.Handle("GET /api/admin/events", dependencies.AdminSettings)
		mux.Handle("GET /api/admin/diagnostics", dependencies.AdminSettings)
	}
	if dependencies.Auth != nil {
		mux.Handle("/api/auth/", dependencies.Auth)
		mux.Handle("/api/admin/accounts/", dependencies.Auth)
	}
	if dependencies.Admin != nil {
		mux.Handle("/api/admin/", dependencies.Admin)
	}
	if dependencies.Invitation != nil {
		mux.Handle("POST /api/auth/registration", dependencies.Invitation)
		mux.Handle("GET /api/invitations", dependencies.Invitation)
		mux.Handle("POST /api/invitations", dependencies.Invitation)
		mux.Handle("DELETE /api/invitations/{id}", dependencies.Invitation)
		mux.Handle("POST /api/admin/invitations", dependencies.Invitation)
		mux.Handle("GET /api/admin/invitations", dependencies.Invitation)
		mux.Handle("DELETE /api/admin/invitations/{id}", dependencies.Invitation)
		mux.Handle("POST /api/admin/accounts/{id}/invitation-quota", dependencies.Invitation)
	}
	if dependencies.Static != nil {
		mux.Handle("GET /{$}", dependencies.Static)
		mux.Handle("GET /hosted.html", dependencies.Static)
		mux.Handle("GET /obs/{publicID}", dependencies.Static)
		mux.Handle("GET /obs/{publicID}/{$}", dependencies.Static)
		mux.Handle("GET /assets/", dependencies.Static)
	}
	return mux
}
