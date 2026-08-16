package biligateway

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"

	"golang.org/x/sync/singleflight"
)

var ErrInvalidRoom = errors.New("invalid_room")

type RoomInfo struct {
	RoomID          string `json:"roomId"`
	CanonicalRoomID string `json:"canonicalRoomId"`
	Title           string `json:"title"`
}
type Sink func(Event)
type Event struct {
	Type string
	Data []byte
}
type Connection interface {
	Close() error
	Done() <-chan struct{}
	Err() error
}
type Status struct {
	CredentialVersion int64 `json:"credentialVersion"`
	EgressOpen        bool  `json:"egressOpen"`
}

// Gateway is the sole production seam permitted to perform Bilibili room I/O.
type Gateway interface {
	RoomInfo(context.Context, string) (RoomInfo, error)
	GiftCatalog(context.Context, string) ([]gameplay.GiftInfo, error)
	OpenRoom(context.Context, string, Sink) (Connection, error)
	Status() Status
}

type upstreamGateway interface {
	RoomInfo(context.Context, string, []byte) (RoomInfo, error)
	GiftCatalog(context.Context, string, []byte) ([]gameplay.GiftInfo, error)
	OpenRoom(context.Context, string, []byte, Sink) (Connection, error)
}
type credentialLoader interface {
	Load(context.Context) (Credential, error)
}
type GatewayOptions struct{ Now func() time.Time }
type accountScopeKey struct{}

// WithAccount attaches a process-internal, trusted account scope. HTTP
// handlers must never derive this from user input. roomsource uses the first
// authorized subscriber's scope when it opens a shared room; later subscribers
// reuse that connection and do not create another upstream request.
func WithAccount(ctx context.Context, accountID int64) context.Context {
	if accountID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, accountScopeKey{}, accountID)
}
func accountFromContext(ctx context.Context) (int64, bool) {
	accountID, ok := ctx.Value(accountScopeKey{}).(int64)
	return accountID, ok && accountID > 0
}

type cachedRoomInfo struct {
	value     RoomInfo
	expiresAt time.Time
}
type cachedCatalog struct {
	value     []gameplay.GiftInfo
	expiresAt time.Time
}

type ControlledGateway struct {
	upstream          upstreamGateway
	credentials       credentialLoader
	now               func() time.Time
	mu                sync.Mutex
	roomInfo          map[string]cachedRoomInfo
	catalog           map[string]cachedCatalog
	flights           singleflight.Group
	breaker           *egressBreaker
	limits            *requestLimiter
	credentialVersion int64
}

func NewControlledGateway(upstream upstreamGateway, credentials credentialLoader, options GatewayOptions) *ControlledGateway {
	if options.Now == nil {
		options.Now = time.Now
	}
	return &ControlledGateway{upstream: upstream, credentials: credentials, now: options.Now, roomInfo: make(map[string]cachedRoomInfo), catalog: make(map[string]cachedCatalog), breaker: newEgressBreaker(options.Now), limits: newRequestLimiter(options.Now)}
}

func (gateway *ControlledGateway) RoomInfo(ctx context.Context, roomID string) (RoomInfo, error) {
	key, err := normalizeRoomID(roomID)
	if err != nil {
		return RoomInfo{}, err
	}
	accountID, scoped := accountFromContext(ctx)
	if !scoped {
		return RoomInfo{}, ErrAccountScopeRequired
	}
	if gateway == nil || gateway.upstream == nil || gateway.credentials == nil {
		return RoomInfo{}, ErrCredentialUnavailable
	}
	if value, ok := gateway.cachedRoom(key); ok {
		return value, nil
	}
	result, err, _ := gateway.flights.Do("room:"+key, func() (any, error) {
		if value, ok := gateway.cachedRoom(key); ok {
			return cachedRoomResult{value: value}, nil
		}
		if !gateway.breaker.Allow(accountID) {
			return nil, ErrEgressUnavailable
		}
		if !gateway.limits.Allow(accountID, "room_info") {
			gateway.breaker.RecordFailure()
			return nil, ErrRateLimited
		}
		credential, loadErr := gateway.credentials.Load(ctx)
		if loadErr != nil {
			gateway.observeEgress(accountID, loadErr)
			return RoomInfo{}, loadErr
		}
		gateway.rememberCredentialVersion(credential.Version)
		defer clear(credential.Cookie)
		value, requestErr := gateway.upstream.RoomInfo(ctx, key, credential.Cookie)
		if requestErr != nil {
			gateway.observeEgress(accountID, requestErr)
			return RoomInfo{}, requestErr
		}
		value.RoomID = key
		if canonical, canonicalErr := normalizeRoomID(value.CanonicalRoomID); canonicalErr == nil {
			value.CanonicalRoomID = canonical
		} else {
			gateway.breaker.RecordFailure()
			return RoomInfo{}, ErrInvalidRoom
		}
		gateway.mu.Lock()
		gateway.roomInfo[key] = cachedRoomInfo{value: value, expiresAt: gateway.now().Add(5 * time.Minute)}
		gateway.mu.Unlock()
		gateway.breaker.RecordSuccess()
		return cachedRoomResult{value: value, egress: true}, nil
	})
	if err != nil {
		return RoomInfo{}, err
	}
	value := result.(cachedRoomResult)
	return value.value, nil
}

func (gateway *ControlledGateway) GiftCatalog(ctx context.Context, roomID string) ([]gameplay.GiftInfo, error) {
	key, err := normalizeRoomID(roomID)
	if err != nil {
		return nil, err
	}
	accountID, scoped := accountFromContext(ctx)
	if !scoped {
		return nil, ErrAccountScopeRequired
	}
	if gateway == nil || gateway.upstream == nil || gateway.credentials == nil {
		return nil, ErrCredentialUnavailable
	}
	if value, ok := gateway.cachedCatalog(key); ok {
		return cloneGiftCatalog(value), nil
	}
	result, err, _ := gateway.flights.Do("gifts:"+key, func() (any, error) {
		if value, ok := gateway.cachedCatalog(key); ok {
			return cachedCatalogResult{value: value}, nil
		}
		if !gateway.breaker.Allow(accountID) {
			return nil, ErrEgressUnavailable
		}
		if !gateway.limits.Allow(accountID, "gift_catalog") {
			gateway.breaker.RecordFailure()
			return nil, ErrRateLimited
		}
		credential, loadErr := gateway.credentials.Load(ctx)
		if loadErr != nil {
			gateway.observeEgress(accountID, loadErr)
			return nil, loadErr
		}
		gateway.rememberCredentialVersion(credential.Version)
		defer clear(credential.Cookie)
		value, requestErr := gateway.upstream.GiftCatalog(ctx, key, credential.Cookie)
		if requestErr != nil {
			gateway.observeEgress(accountID, requestErr)
			return nil, requestErr
		}
		value = cloneGiftCatalog(value)
		gateway.mu.Lock()
		gateway.catalog[key] = cachedCatalog{value: value, expiresAt: gateway.now().Add(10 * time.Minute)}
		gateway.mu.Unlock()
		gateway.breaker.RecordSuccess()
		return cachedCatalogResult{value: value, egress: true}, nil
	})
	if err != nil {
		return nil, err
	}
	value := result.(cachedCatalogResult)
	return cloneGiftCatalog(value.value), nil
}

func (gateway *ControlledGateway) OpenRoom(ctx context.Context, roomID string, sink Sink) (Connection, error) {
	key, err := normalizeRoomID(roomID)
	if err != nil {
		return nil, err
	}
	accountID, scoped := accountFromContext(ctx)
	if !scoped {
		return nil, ErrAccountScopeRequired
	}
	if gateway == nil || gateway.upstream == nil || gateway.credentials == nil || sink == nil {
		return nil, ErrCredentialUnavailable
	}
	if !gateway.breaker.Allow(accountID) {
		return nil, ErrEgressUnavailable
	}
	if !gateway.limits.Allow(accountID, "open_room") {
		gateway.breaker.RecordFailure()
		return nil, ErrRateLimited
	}
	credential, err := gateway.credentials.Load(ctx)
	if err != nil {
		gateway.observeEgress(accountID, err)
		return nil, err
	}
	gateway.rememberCredentialVersion(credential.Version)
	defer clear(credential.Cookie)
	connection, openErr := gateway.upstream.OpenRoom(ctx, key, credential.Cookie, sink)
	if openErr != nil {
		gateway.observeEgress(accountID, openErr)
		return nil, openErr
	}
	gateway.breaker.RecordSuccess()
	return connection, nil
}
func (gateway *ControlledGateway) observeEgress(accountID int64, err error) {
	if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrRiskRejected) {
		gateway.breaker.RecordRisk(accountID)
		return
	}
	gateway.breaker.RecordFailure()
}

type cachedRoomResult struct {
	value  RoomInfo
	egress bool
}
type cachedCatalogResult struct {
	value  []gameplay.GiftInfo
	egress bool
}

func (gateway *ControlledGateway) Status() Status {
	if gateway == nil {
		return Status{}
	}
	gateway.mu.Lock()
	version := gateway.credentialVersion
	gateway.mu.Unlock()
	return Status{CredentialVersion: version, EgressOpen: gateway.breaker.Open()}
}
func (gateway *ControlledGateway) rememberCredentialVersion(version int64) {
	if version <= 0 {
		return
	}
	gateway.mu.Lock()
	gateway.credentialVersion = version
	gateway.mu.Unlock()
}
func (gateway *ControlledGateway) cachedRoom(key string) (RoomInfo, bool) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	value, ok := gateway.roomInfo[key]
	return value.value, ok && gateway.now().Before(value.expiresAt)
}
func (gateway *ControlledGateway) cachedCatalog(key string) ([]gameplay.GiftInfo, bool) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	value, ok := gateway.catalog[key]
	return value.value, ok && gateway.now().Before(value.expiresAt)
}
func cloneGiftCatalog(input []gameplay.GiftInfo) []gameplay.GiftInfo {
	return append([]gameplay.GiftInfo(nil), input...)
}
func normalizeRoomID(input string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" || len(value) > 128 {
		return "", ErrInvalidRoom
	}
	numeric, err := strconv.ParseUint(value, 10, 64)
	if err != nil || numeric == 0 {
		return "", ErrInvalidRoom
	}
	return strconv.FormatUint(numeric, 10), nil
}
