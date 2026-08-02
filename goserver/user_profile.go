package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultBilibiliUserProfileEndpoint = "https://api.bilibili.com/x/web-interface/card"

type userProfile struct {
	UID    int64
	Name   string
	Avatar string
}

type userProfileResolver interface {
	Resolve(context.Context, int64) (userProfile, error)
}

type userProfileCacheEntry struct {
	profile      userProfile
	errorMessage string
	expiresAt    time.Time
}

type bilibiliUserProfileResolver struct {
	client   *http.Client
	endpoint string
	now      func() time.Time
	mu       sync.RWMutex
	cache    map[int64]userProfileCacheEntry
}

func newBilibiliUserProfileResolver(client *http.Client, endpoint string) *bilibiliUserProfileResolver {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	if strings.TrimSpace(endpoint) == "" {
		endpoint = defaultBilibiliUserProfileEndpoint
	}
	return &bilibiliUserProfileResolver{
		client: client, endpoint: endpoint, now: time.Now, cache: map[int64]userProfileCacheEntry{},
	}
}

func (resolver *bilibiliUserProfileResolver) Resolve(ctx context.Context, uid int64) (userProfile, error) {
	if uid <= 0 {
		return userProfile{}, fmt.Errorf("用户 UID 无效")
	}
	if cached, ok := resolver.cached(uid); ok {
		if cached.errorMessage != "" {
			return userProfile{}, errors.New(cached.errorMessage)
		}
		return cached.profile, nil
	}

	profile, err := resolver.fetch(ctx, uid)
	entry := userProfileCacheEntry{profile: profile, expiresAt: resolver.now().Add(6 * time.Hour)}
	if err != nil {
		entry.profile = userProfile{}
		entry.errorMessage = err.Error()
		entry.expiresAt = resolver.now().Add(5 * time.Minute)
	}
	resolver.mu.Lock()
	resolver.cache[uid] = entry
	resolver.mu.Unlock()
	return profile, err
}

func (resolver *bilibiliUserProfileResolver) cached(uid int64) (userProfileCacheEntry, bool) {
	resolver.mu.RLock()
	entry, exists := resolver.cache[uid]
	resolver.mu.RUnlock()
	if !exists || !resolver.now().Before(entry.expiresAt) {
		if exists {
			resolver.mu.Lock()
			delete(resolver.cache, uid)
			resolver.mu.Unlock()
		}
		return userProfileCacheEntry{}, false
	}
	return entry, true
}

func (resolver *bilibiliUserProfileResolver) fetch(ctx context.Context, uid int64) (userProfile, error) {
	endpoint, err := url.Parse(resolver.endpoint)
	if err != nil {
		return userProfile{}, fmt.Errorf("用户资料接口地址无效：%w", err)
	}
	query := endpoint.Query()
	query.Set("mid", strconv.FormatInt(uid, 10))
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return userProfile{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", fmt.Sprintf("https://space.bilibili.com/%d", uid))
	response, err := resolver.client.Do(req)
	if err != nil {
		return userProfile{}, fmt.Errorf("用户资料请求失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return userProfile{}, fmt.Errorf("用户资料请求返回 HTTP %d", response.StatusCode)
	}
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Card struct {
				Name string `json:"name"`
				Face string `json:"face"`
			} `json:"card"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return userProfile{}, fmt.Errorf("用户资料响应解析失败：%w", err)
	}
	if payload.Code != 0 {
		return userProfile{}, fmt.Errorf("用户资料接口错误 %d：%s", payload.Code, payload.Message)
	}
	profile := userProfile{UID: uid, Name: strings.TrimSpace(payload.Data.Card.Name), Avatar: strings.TrimSpace(payload.Data.Card.Face)}
	if profile.Name == "" && profile.Avatar == "" {
		return userProfile{}, fmt.Errorf("用户资料为空")
	}
	return profile, nil
}

func needsUserProfile(gift giftEvent) bool {
	return gift.UID > 0 && (isMaskedUsername(gift.Uname) || strings.TrimSpace(gift.Avatar) == "")
}

func isMaskedUsername(name string) bool {
	name = strings.TrimSpace(name)
	return name == "" || strings.Contains(name, "*") || strings.Contains(name, "＊")
}
