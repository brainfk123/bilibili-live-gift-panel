package main

import (
	"net/http"
	"strconv"
)

const maxBlindBoxLeaderboardLimit = 2000

func handleBlindBoxLeaderboard(store *configStore, diagnostics *diagnosticLogger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}

		query, err := parseBlindBoxLeaderboardQuery(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "排行榜参数无效"})
			return
		}

		state, err := store.readState()
		if err != nil {
			if diagnostics != nil {
				diagnostics.Error("blind_box_leaderboard_read_failed", "error", err, "error_kind", "read")
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"code": -1, "message": "排行榜读取失败，请重试。",
			})
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 0, "leaderboard": buildBlindBoxLeaderboard(state.Contributions, query),
		})
	}
}

func parseBlindBoxLeaderboardQuery(r *http.Request) (blindBoxLeaderboardQuery, error) {
	query := blindBoxLeaderboardQuery{}
	if giftID, present, err := parseBlindBoxLeaderboardQueryInteger(r, "giftId"); err != nil {
		return blindBoxLeaderboardQuery{}, err
	} else if present {
		if giftID <= 0 {
			return blindBoxLeaderboardQuery{}, errBlindBoxLeaderboardQuery
		}
		query.GiftID = giftID
	}
	if limit, present, err := parseBlindBoxLeaderboardQueryInteger(r, "limit"); err != nil {
		return blindBoxLeaderboardQuery{}, err
	} else if present {
		if limit < 0 || limit > maxBlindBoxLeaderboardLimit {
			return blindBoxLeaderboardQuery{}, errBlindBoxLeaderboardQuery
		}
		query.Limit = limit
		query.HasLimit = true
	}
	return query, nil
}

var errBlindBoxLeaderboardQuery = blindBoxLeaderboardQueryError{}

type blindBoxLeaderboardQueryError struct{}

func (blindBoxLeaderboardQueryError) Error() string { return "invalid blind box leaderboard query" }

func parseBlindBoxLeaderboardQueryInteger(r *http.Request, name string) (int, bool, error) {
	values, present := r.URL.Query()[name]
	if !present {
		return 0, false, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, true, errBlindBoxLeaderboardQuery
	}
	value, err := strconv.Atoi(values[0])
	if err != nil {
		return 0, true, errBlindBoxLeaderboardQuery
	}
	return value, true, nil
}
