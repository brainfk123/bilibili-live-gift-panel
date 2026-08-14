package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	attributeEditLeaseTTL          = 15 * time.Second
	maxAttributeEditLeaseBodyBytes = 4 << 10
	attributeEditLeaseTokenBytes   = 18
	attributeEditLeaseTokenLength  = 24
)

type attributeFreezeChecker interface {
	IsFrozen(attributeID string) bool
}

type attributeEditLease struct {
	attributeID string
	expiresAt   time.Time
	claims      int
	releasing   bool
	expired     bool
}

type attributeEditLeaseCoordinator struct {
	mu                 sync.Mutex
	changed            *sync.Cond
	ttl                time.Duration
	now                func() time.Time
	newToken           func() (string, error)
	sessions           map[string]*attributeEditLease
	afterBegin         func()
	afterReleaseMarked func()
}

func newAttributeEditLeaseCoordinator(ttl time.Duration, now func() time.Time, token func() (string, error)) *attributeEditLeaseCoordinator {
	leases := &attributeEditLeaseCoordinator{
		ttl:      ttl,
		now:      now,
		newToken: token,
		sessions: map[string]*attributeEditLease{},
	}
	leases.changed = sync.NewCond(&leases.mu)
	return leases
}

func newDefaultAttributeEditLeaseCoordinator() *attributeEditLeaseCoordinator {
	return newAttributeEditLeaseCoordinator(attributeEditLeaseTTL, time.Now, newAttributeEditLeaseToken)
}

func newAttributeEditLeaseToken() (string, error) {
	bytes := make([]byte, attributeEditLeaseTokenBytes)
	if _, err := io.ReadFull(rand.Reader, bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func (leases *attributeEditLeaseCoordinator) Create(attributeID string) (string, time.Time, error) {
	attributeID = strings.TrimSpace(attributeID)
	leases.mu.Lock()
	defer leases.mu.Unlock()
	now := leases.now()
	leases.removeExpiredLocked(now)

	token, err := leases.newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := now.Add(leases.ttl)
	leases.sessions[token] = &attributeEditLease{attributeID: attributeID, expiresAt: expiresAt}
	return token, expiresAt, nil
}

func (leases *attributeEditLeaseCoordinator) Renew(attributeID, token string) (time.Time, bool) {
	attributeID = strings.TrimSpace(attributeID)
	token = strings.TrimSpace(token)
	leases.mu.Lock()
	defer leases.mu.Unlock()
	now := leases.now()
	leases.removeExpiredLocked(now)

	lease, ok := leases.sessions[token]
	if !ok || lease.attributeID != attributeID || lease.releasing || lease.expired {
		return time.Time{}, false
	}
	lease.expiresAt = now.Add(leases.ttl)
	return lease.expiresAt, true
}

// Has reports whether token currently owns a live lease for attributeID.
func (leases *attributeEditLeaseCoordinator) Has(attributeID, token string) bool {
	attributeID = strings.TrimSpace(attributeID)
	token = strings.TrimSpace(token)
	leases.mu.Lock()
	defer leases.mu.Unlock()
	leases.removeExpiredLocked(leases.now())
	lease, ok := leases.sessions[token]
	return ok && !lease.releasing && !lease.expired && lease.attributeID == attributeID
}

type attributeEditLeaseClaim struct {
	coordinator *attributeEditLeaseCoordinator
	token       string
	lease       *attributeEditLease
	once        sync.Once
}

// Begin grants an in-flight claim without retaining leases.mu. Release waits
// for the claim to finish, so a valid token cannot be revoked between the
// authorization check and a store mutation.
func (leases *attributeEditLeaseCoordinator) Begin(attributeID, token string) (*attributeEditLeaseClaim, bool) {
	attributeID = strings.TrimSpace(attributeID)
	token = strings.TrimSpace(token)
	leases.mu.Lock()
	leases.removeExpiredLocked(leases.now())
	lease, ok := leases.sessions[token]
	if !ok || lease.releasing || lease.expired || lease.attributeID != attributeID {
		leases.mu.Unlock()
		return nil, false
	}
	lease.claims++
	claim := &attributeEditLeaseClaim{coordinator: leases, token: token, lease: lease}
	hook := leases.afterBegin
	leases.mu.Unlock()
	if hook != nil {
		hook()
	}
	return claim, true
}

func (claim *attributeEditLeaseClaim) Live() bool {
	if claim == nil || claim.coordinator == nil || claim.lease == nil {
		return false
	}
	leases := claim.coordinator
	leases.mu.Lock()
	defer leases.mu.Unlock()
	leases.removeExpiredLocked(leases.now())
	return leases.sessions[claim.token] == claim.lease && !claim.lease.expired && claim.lease.claims > 0
}

func (claim *attributeEditLeaseClaim) Finish() {
	if claim == nil || claim.coordinator == nil || claim.lease == nil {
		return
	}
	claim.once.Do(func() {
		leases := claim.coordinator
		leases.mu.Lock()
		defer leases.mu.Unlock()
		if claim.lease.claims > 0 {
			claim.lease.claims--
		}
		if claim.lease.claims == 0 && (claim.lease.releasing || claim.lease.expired || !claim.lease.expiresAt.After(leases.now())) {
			if leases.sessions[claim.token] == claim.lease {
				delete(leases.sessions, claim.token)
			}
		}
		leases.changed.Broadcast()
	})
}

func (leases *attributeEditLeaseCoordinator) Release(attributeID, token string) bool {
	attributeID = strings.TrimSpace(attributeID)
	token = strings.TrimSpace(token)
	leases.mu.Lock()
	leases.removeExpiredLocked(leases.now())

	lease, ok := leases.sessions[token]
	if !ok || lease.attributeID != attributeID {
		leases.mu.Unlock()
		return false
	}
	lease.releasing = true
	hook := leases.afterReleaseMarked
	leases.mu.Unlock()
	if hook != nil {
		hook()
	}
	leases.mu.Lock()
	for lease.claims > 0 {
		leases.changed.Wait()
	}
	delete(leases.sessions, token)
	leases.mu.Unlock()
	return true
}

func (leases *attributeEditLeaseCoordinator) IsFrozen(attributeID string) bool {
	attributeID = strings.TrimSpace(attributeID)
	leases.mu.Lock()
	defer leases.mu.Unlock()
	leases.removeExpiredLocked(leases.now())
	for _, lease := range leases.sessions {
		if lease.attributeID == attributeID && !lease.expired {
			return true
		}
	}
	return false
}

func (leases *attributeEditLeaseCoordinator) removeExpiredLocked(now time.Time) {
	for token, lease := range leases.sessions {
		if !lease.expiresAt.After(now) && lease.claims == 0 {
			lease.expired = true
			delete(leases.sessions, token)
			continue
		}
		if !lease.expiresAt.After(now) {
			lease.expired = true
		}
	}
}

type attributeEditLeaseRequest struct {
	AttributeID string `json:"attributeId"`
	Token       string `json:"token,omitempty"`
}

func newAttributeEditLeaseHandler(store *configStore, leases *attributeEditLeaseCoordinator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/api/attribute-edit-lease" {
			attributeEditLeaseWriteError(w, http.StatusNotFound, "请求地址不存在")
			return
		}
		if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "POST, PUT, DELETE")
			attributeEditLeaseWriteError(w, http.StatusMethodNotAllowed, "不支持的请求方法")
			return
		}
		if !isSameOriginGiftReceiptRequest(r) {
			attributeEditLeaseWriteError(w, http.StatusForbidden, "拒绝跨站请求")
			return
		}

		request, status, message := decodeAttributeEditLeaseRequest(w, r)
		if status != 0 {
			attributeEditLeaseWriteError(w, status, message)
			return
		}
		request.AttributeID = strings.TrimSpace(request.AttributeID)
		if !validAttributeEditLeaseAttributeID(request.AttributeID) {
			attributeEditLeaseWriteError(w, http.StatusBadRequest, "属性 ID 无效")
			return
		}

		switch r.Method {
		case http.MethodPost:
			if request.Token != "" {
				attributeEditLeaseWriteError(w, http.StatusBadRequest, "请求格式不正确")
				return
			}
			if !attributeEditLeaseAttributeExists(store, request.AttributeID) {
				attributeEditLeaseWriteError(w, http.StatusNotFound, "属性不存在")
				return
			}
			token, expiresAt, err := leases.Create(request.AttributeID)
			if err != nil {
				attributeEditLeaseWriteError(w, http.StatusInternalServerError, "创建编辑租约失败")
				return
			}
			attributeEditLeaseWriteJSON(w, http.StatusOK, map[string]any{"code": 0, "token": token, "expiresAt": expiresAt})
		case http.MethodPut:
			if !validAttributeEditLeaseToken(request.Token) {
				attributeEditLeaseWriteError(w, http.StatusBadRequest, "租约令牌无效")
				return
			}
			if !attributeEditLeaseAttributeExists(store, request.AttributeID) {
				attributeEditLeaseWriteError(w, http.StatusNotFound, "属性不存在")
				return
			}
			expiresAt, ok := leases.Renew(request.AttributeID, request.Token)
			if !ok {
				attributeEditLeaseWriteError(w, http.StatusNotFound, "编辑租约不存在")
				return
			}
			attributeEditLeaseWriteJSON(w, http.StatusOK, map[string]any{"code": 0, "expiresAt": expiresAt})
		case http.MethodDelete:
			if !validAttributeEditLeaseToken(request.Token) {
				attributeEditLeaseWriteError(w, http.StatusBadRequest, "租约令牌无效")
				return
			}
			leases.Release(request.AttributeID, request.Token)
			attributeEditLeaseWriteJSON(w, http.StatusOK, map[string]any{"code": 0})
		}
	})
}

func decodeAttributeEditLeaseRequest(w http.ResponseWriter, r *http.Request) (attributeEditLeaseRequest, int, string) {
	reader := http.MaxBytesReader(w, r.Body, maxAttributeEditLeaseBodyBytes)
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var request attributeEditLeaseRequest
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return attributeEditLeaseRequest{}, http.StatusRequestEntityTooLarge, "请求内容过大"
		}
		return attributeEditLeaseRequest{}, http.StatusBadRequest, "请求格式不正确"
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return attributeEditLeaseRequest{}, http.StatusRequestEntityTooLarge, "请求内容过大"
		}
		return attributeEditLeaseRequest{}, http.StatusBadRequest, "请求格式不正确"
	}
	return request, 0, ""
}

func validAttributeEditLeaseAttributeID(attributeID string) bool {
	return utf8.ValidString(attributeID) && len(attributeID) >= 1 && len(attributeID) <= 160
}

func validAttributeEditLeaseToken(token string) bool {
	if len(token) != attributeEditLeaseTokenLength {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == attributeEditLeaseTokenBytes
}

func attributeEditLeaseAttributeExists(store *configStore, attributeID string) bool {
	state, err := store.readState()
	if err != nil {
		return false
	}
	matches := 0
	for _, attribute := range state.Attributes {
		if attribute.ID == attributeID {
			matches++
		}
	}
	return matches == 1
}

func attributeEditLeaseWriteError(w http.ResponseWriter, status int, message string) {
	attributeEditLeaseWriteJSON(w, status, map[string]any{"code": -1, "message": message})
}

func attributeEditLeaseWriteJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
