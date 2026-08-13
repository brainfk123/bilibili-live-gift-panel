package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlindBoxLeaderboardHTTPReturnsAuthoritativeSnapshot(t *testing.T) {
	store := blindBoxLeaderboardHTTPStore(t, contributionLedgerState{
		UpdatedAt: 1700000000000,
		Viewers: []viewerContribution{
			{Key: "uid:1", UID: 1, Uname: "甲", BlindBoxCount: 2, BlindBoxCost: 100, BlindBoxValue: 240, BlindBoxes: []blindBoxContribution{{GiftID: 7, GiftName: "盲盒七", Count: 2, Cost: 100, Value: 240, LastGiftAt: 1700000000000}}, LastGiftAt: 1700000000000},
			{Key: "uid:2", UID: 2, Uname: "乙", BlindBoxCount: 1, BlindBoxCost: 100, BlindBoxValue: 40, BlindBoxes: []blindBoxContribution{{GiftID: 7, GiftName: "盲盒七", Count: 1, Cost: 100, Value: 40, LastGiftAt: 1700000001000}}, LastGiftAt: 1700000001000},
			{Key: "uid:3", UID: 3, Uname: "普通礼物", GiftCount: 1, LastGiftAt: 1700000002000},
		},
	})

	for _, test := range []struct {
		name    string
		query   string
		wantIDs []int64
	}{
		{name: "all scopes", wantIDs: []int64{1, 2}},
		{name: "one gift scope", query: "?giftId=7", wantIDs: []int64{1, 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handleBlindBoxLeaderboard(store, nil)(response, httptest.NewRequest(http.MethodGet, "/api/blind-box/leaderboard"+test.query, nil))

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			payload := decodeBlindBoxLeaderboardHTTPResponse(t, response)
			if payload.Code != 0 {
				t.Fatalf("code = %d, want 0", payload.Code)
			}
			if got, want := payload.Leaderboard.UpdatedAt, int64(1700000000000); got != want {
				t.Fatalf("updatedAt = %d, want %d", got, want)
			}
			if got, want := payload.Leaderboard.Summary, (blindBoxLeaderboardSummary{ViewerCount: 2, BlindBoxCount: 3, Cost: 200, Value: 280, Profit: 80}); got != want {
				t.Fatalf("summary = %#v, want %#v", got, want)
			}
			if len(payload.Leaderboard.Viewers) != len(test.wantIDs) {
				t.Fatalf("viewers = %#v, want %d viewers", payload.Leaderboard.Viewers, len(test.wantIDs))
			}
			for index, want := range test.wantIDs {
				if got := payload.Leaderboard.Viewers[index].UID; got != want {
					t.Fatalf("viewer[%d].uid = %d, want %d", index, got, want)
				}
			}
			if got, want := payload.Leaderboard.Scopes, []blindBoxLeaderboardScope{{GiftID: 7, GiftName: "盲盒七", Count: 3, LastGiftAt: 1700000001000}}; !equalBlindBoxLeaderboardScopes(got, want) {
				t.Fatalf("scopes = %#v, want %#v", got, want)
			}
		})
	}
}

func TestBlindBoxLeaderboardHTTPAcceptsZeroLimitWithoutTruncatingSummary(t *testing.T) {
	store := blindBoxLeaderboardHTTPStore(t, contributionLedgerState{
		Viewers: []viewerContribution{
			{Key: "uid:1", UID: 1, Uname: "甲", BlindBoxCount: 2, BlindBoxCost: 100, BlindBoxValue: 240, BlindBoxes: []blindBoxContribution{{GiftID: 7, GiftName: "盲盒七", Count: 2, Cost: 100, Value: 240}}, LastGiftAt: 1700000000000},
			{Key: "uid:2", UID: 2, Uname: "乙", BlindBoxCount: 1, BlindBoxCost: 100, BlindBoxValue: 40, BlindBoxes: []blindBoxContribution{{GiftID: 7, GiftName: "盲盒七", Count: 1, Cost: 100, Value: 40}}, LastGiftAt: 1700000001000},
		},
	})

	response := httptest.NewRecorder()
	handleBlindBoxLeaderboard(store, nil)(response, httptest.NewRequest(http.MethodGet, "/api/blind-box/leaderboard?limit=0", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	payload := decodeBlindBoxLeaderboardHTTPResponse(t, response)
	if len(payload.Leaderboard.Viewers) != 0 {
		t.Fatalf("limited viewers = %#v, want none", payload.Leaderboard.Viewers)
	}
	if got, want := payload.Leaderboard.Summary, (blindBoxLeaderboardSummary{ViewerCount: 2, BlindBoxCount: 3, Cost: 200, Value: 280, Profit: 80}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	if got, want := payload.Leaderboard.Scopes, []blindBoxLeaderboardScope{{GiftID: 7, GiftName: "盲盒七", Count: 3}}; !equalBlindBoxLeaderboardScopes(got, want) {
		t.Fatalf("scopes = %#v, want %#v", got, want)
	}
}

func TestBlindBoxLeaderboardHTTPAcceptsMaximumLimitAndCombinedQuery(t *testing.T) {
	store := blindBoxLeaderboardHTTPStore(t, contributionLedgerState{
		Viewers: []viewerContribution{
			{Key: "uid:1", UID: 1, Uname: "甲", BlindBoxCount: 2, BlindBoxCost: 100, BlindBoxValue: 240, BlindBoxes: []blindBoxContribution{{GiftID: 7, GiftName: "盲盒七", Count: 2, Cost: 100, Value: 240}}, LastGiftAt: 1700000000000},
			{Key: "uid:2", UID: 2, Uname: "乙", BlindBoxCount: 1, BlindBoxCost: 100, BlindBoxValue: 40, BlindBoxes: []blindBoxContribution{{GiftID: 7, GiftName: "盲盒七", Count: 1, Cost: 100, Value: 40}}, LastGiftAt: 1700000001000},
		},
	})
	for _, test := range []struct {
		name          string
		query         string
		wantViewerIDs []int64
	}{
		{name: "maximum limit", query: "?limit=2000", wantViewerIDs: []int64{1, 2}},
		{name: "gift and limit", query: "?giftId=7&limit=1", wantViewerIDs: []int64{1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handleBlindBoxLeaderboard(store, nil)(response, httptest.NewRequest(http.MethodGet, "/api/blind-box/leaderboard"+test.query, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			payload := decodeBlindBoxLeaderboardHTTPResponse(t, response)
			if got, want := payload.Leaderboard.Summary, (blindBoxLeaderboardSummary{ViewerCount: 2, BlindBoxCount: 3, Cost: 200, Value: 280, Profit: 80}); got != want {
				t.Fatalf("summary = %#v, want %#v", got, want)
			}
			if len(payload.Leaderboard.Viewers) != len(test.wantViewerIDs) {
				t.Fatalf("viewers = %#v, want %d", payload.Leaderboard.Viewers, len(test.wantViewerIDs))
			}
			for index, want := range test.wantViewerIDs {
				if got := payload.Leaderboard.Viewers[index].UID; got != want {
					t.Fatalf("viewer[%d].uid = %d, want %d", index, got, want)
				}
			}
		})
	}
}

func TestBlindBoxLeaderboardHTTPRejectsInvalidQueries(t *testing.T) {
	store := blindBoxLeaderboardHTTPStore(t, contributionLedgerState{})
	for _, query := range []string{
		"?giftId=", "?giftId=0", "?giftId=-1", "?giftId=1.5",
		"?limit=", "?limit=-1", "?limit=2001", "?limit=1.5",
	} {
		t.Run(query, func(t *testing.T) {
			response := httptest.NewRecorder()
			handleBlindBoxLeaderboard(store, nil)(response, httptest.NewRequest(http.MethodGet, "/api/blind-box/leaderboard"+query, nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if payload := decodeBlindBoxLeaderboardHTTPError(t, response); payload != (blindBoxLeaderboardHTTPError{Code: -1, Message: "排行榜参数无效"}) {
				t.Fatalf("error = %#v, want stable public client error", payload)
			}
		})
	}
}

func TestBlindBoxLeaderboardHTTPRejectsDuplicateQueries(t *testing.T) {
	store := blindBoxLeaderboardHTTPStore(t, contributionLedgerState{})
	for _, query := range []string{"?giftId=1&giftId=2", "?limit=1&limit=2"} {
		t.Run(query, func(t *testing.T) {
			response := httptest.NewRecorder()
			handleBlindBoxLeaderboard(store, nil)(response, httptest.NewRequest(http.MethodGet, "/api/blind-box/leaderboard"+query, nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if payload := decodeBlindBoxLeaderboardHTTPError(t, response); payload != (blindBoxLeaderboardHTTPError{Code: -1, Message: "排行榜参数无效"}) {
				t.Fatalf("error = %#v, want stable public client error", payload)
			}
		})
	}
}

func TestBlindBoxLeaderboardHTTPAllowsOnlyGet(t *testing.T) {
	store := blindBoxLeaderboardHTTPStore(t, contributionLedgerState{})
	response := httptest.NewRecorder()
	handleBlindBoxLeaderboard(store, nil)(response, httptest.NewRequest(http.MethodPost, "/api/blind-box/leaderboard", strings.NewReader(`{"giftId":7}`)))

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", got)
	}
	if got := decodeBlindBoxLeaderboardHTTPError(t, response); got != (blindBoxLeaderboardHTTPError{Code: -1, Message: "不支持的请求方法"}) {
		t.Fatalf("error = %#v", got)
	}
}

func TestBlindBoxLeaderboardHTTPHidesStoreErrorsAndRecordsCause(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{invalid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &configStore{path: configPath}
	diagnostics, err := newDiagnosticLogger(filepath.Join(root, "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handleBlindBoxLeaderboard(store, diagnostics)(response, httptest.NewRequest(http.MethodGet, "/api/blind-box/leaderboard", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := decodeBlindBoxLeaderboardHTTPError(t, response); got != (blindBoxLeaderboardHTTPError{Code: -1, Message: "排行榜读取失败，请重试。"}) {
		t.Fatalf("error = %#v", got)
	}
	if body := response.Body.String(); strings.Contains(body, configPath) || strings.Contains(body, "invalid character") {
		t.Fatalf("public error leaked store cause: %s", body)
	}
	log, err := os.ReadFile(diagnostics.path)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(log); !strings.Contains(text, "blind_box_leaderboard_read_failed") || !strings.Contains(text, `error_kind="config_decode"`) {
		t.Fatalf("diagnostic event/category missing: %s", log)
	} else if strings.Contains(text, configPath) || strings.Contains(text, "invalid character") || strings.Contains(text, "{invalid json") {
		t.Fatalf("diagnostic log leaked read cause: %s", text)
	}
}

func TestBlindBoxLeaderboardReadErrorKindUsesSafeCategories(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "config decode", err: &json.SyntaxError{}, want: "config_decode"},
		{name: "unsupported version", err: &unsupportedStateVersionError{Shard: "主配置", Version: 999}, want: "unsupported_version"},
		{name: "filesystem read", err: &fs.PathError{Op: "read", Path: "private", Err: fs.ErrPermission}, want: "filesystem_read"},
		{name: "other read", err: errors.New("private cause"), want: "read"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := blindBoxLeaderboardReadErrorKind(test.err); got != test.want {
				t.Fatalf("error kind = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBlindBoxLeaderboardHTTPRegistersMuxRouteWithoutReplacingMetadataRoute(t *testing.T) {
	store := blindBoxLeaderboardHTTPStore(t, contributionLedgerState{})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/blind-box", handleBlindBoxInfo(nil))
	registerBlindBoxLeaderboardRoute(mux, store, nil)

	leaderboard := httptest.NewRecorder()
	mux.ServeHTTP(leaderboard, httptest.NewRequest(http.MethodGet, "/api/blind-box/leaderboard", nil))
	if leaderboard.Code != http.StatusOK {
		t.Fatalf("leaderboard status = %d, body = %s", leaderboard.Code, leaderboard.Body.String())
	}
	if got := decodeBlindBoxLeaderboardHTTPResponse(t, leaderboard).Code; got != 0 {
		t.Fatalf("leaderboard code = %d, want 0", got)
	}

	metadata := httptest.NewRecorder()
	mux.ServeHTTP(metadata, httptest.NewRequest(http.MethodGet, "/api/blind-box", nil))
	if metadata.Code != http.StatusBadRequest {
		t.Fatalf("metadata status = %d, body = %s", metadata.Code, metadata.Body.String())
	}
	if got := decodeBlindBoxLeaderboardHTTPError(t, metadata); got != (blindBoxLeaderboardHTTPError{Code: -1, Message: "礼物 ID 无效"}) {
		t.Fatalf("metadata error = %#v", got)
	}
}

type blindBoxLeaderboardHTTPResponse struct {
	Code        int                         `json:"code"`
	Leaderboard blindBoxLeaderboardSnapshot `json:"leaderboard"`
}

type blindBoxLeaderboardHTTPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func blindBoxLeaderboardHTTPStore(t *testing.T, ledger contributionLedgerState) *configStore {
	t.Helper()
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Contributions = ledger
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	return store
}

func decodeBlindBoxLeaderboardHTTPResponse(t *testing.T, response *httptest.ResponseRecorder) blindBoxLeaderboardHTTPResponse {
	t.Helper()
	var payload blindBoxLeaderboardHTTPResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, response.Body.String())
	}
	return payload
}

func decodeBlindBoxLeaderboardHTTPError(t *testing.T, response *httptest.ResponseRecorder) blindBoxLeaderboardHTTPError {
	t.Helper()
	var payload blindBoxLeaderboardHTTPError
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, response.Body.String())
	}
	return payload
}

func equalBlindBoxLeaderboardScopes(got, want []blindBoxLeaderboardScope) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
