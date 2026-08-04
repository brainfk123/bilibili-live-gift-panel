package main

import (
	"context"
	"net/http"
	"strings"
)

type roomOwnerUIDResolver func(context.Context, string) (int64, error)

func newRoomAnchorHandler(resolveOwnerUID roomOwnerUIDResolver, profiles userProfileResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		roomID := strings.TrimSpace(r.URL.Query().Get("roomId"))
		if roomID == "" {
			roomID = strings.TrimSpace(r.URL.Query().Get("room_id"))
		}
		if roomID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "缺少房间号"})
			return
		}
		if resolveOwnerUID == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": "主播信息服务未初始化"})
			return
		}
		uid, err := resolveOwnerUID(r.Context(), roomID)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"code": -1, "message": err.Error()})
			return
		}

		anchor := map[string]any{"uid": uid}
		if profiles != nil {
			if profile, profileErr := profiles.Resolve(r.Context(), uid); profileErr == nil {
				if strings.TrimSpace(profile.Name) != "" {
					anchor["uname"] = strings.TrimSpace(profile.Name)
				}
				if strings.TrimSpace(profile.Avatar) != "" {
					anchor["avatar"] = strings.TrimSpace(profile.Avatar)
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "roomId": roomID, "anchor": anchor})
	}
}
