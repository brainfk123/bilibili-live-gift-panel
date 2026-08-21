package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type roomAnchorProfileResolver struct {
	profile userProfile
	err     error
}

func (resolver roomAnchorProfileResolver) Resolve(context.Context, int64) (userProfile, error) {
	return resolver.profile, resolver.err
}

func TestRoomAnchorHandlerReturnsBroadcasterProfile(t *testing.T) {
	handler := newRoomAnchorHandler(
		func(_ context.Context, roomID string) (int64, error) {
			if roomID != "31567150" {
				t.Fatalf("roomID = %q", roomID)
			}
			return 32249588, nil
		},
		roomAnchorProfileResolver{profile: userProfile{UID: 32249588, Name: "测试主播", Avatar: "https://example.test/avatar.png"}},
	)
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/api/room/anchor?roomId=31567150", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Code   int    `json:"code"`
		RoomID string `json:"roomId"`
		Anchor struct {
			UID    int64  `json:"uid"`
			Uname  string `json:"uname"`
			Avatar string `json:"avatar"`
		} `json:"anchor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != 0 || payload.RoomID != "31567150" || payload.Anchor.UID != 32249588 || payload.Anchor.Uname != "测试主播" || payload.Anchor.Avatar == "" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestRoomAnchorHandlerKeepsUIDWhenProfileLookupFails(t *testing.T) {
	handler := newRoomAnchorHandler(
		func(context.Context, string) (int64, error) { return 32249588, nil },
		roomAnchorProfileResolver{err: errors.New("profile unavailable")},
	)
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/api/room/anchor?roomId=31567150", nil))
	if response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	anchor := payload["anchor"].(map[string]any)
	if anchor["uid"] != float64(32249588) || anchor["uname"] != nil || anchor["avatar"] != nil {
		t.Fatalf("fallback anchor = %#v", anchor)
	}
}

func TestRoomNotificationProfileResolverUsesBroadcasterProfile(t *testing.T) {
	resolver := newRoomNotificationProfileResolver(
		func(_ context.Context, roomID string) (int64, error) {
			if roomID != "room-a" {
				t.Fatalf("room ID = %q", roomID)
			}
			return 32249588, nil
		},
		roomAnchorProfileResolver{profile: userProfile{
			UID: 32249588, Name: " 测试主播 ", Avatar: " https://example.test/avatar.png ",
		}},
	)
	profile, err := resolver(context.Background(), "room-a")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "测试主播" || profile.AvatarURL != "https://example.test/avatar.png" {
		t.Fatalf("notification profile = %#v", profile)
	}
}
