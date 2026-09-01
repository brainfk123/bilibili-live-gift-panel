package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	updateTrustCacheFilename = "publisher-policy-cache.json"
	updateTrustCacheVersion  = 1
	maxUpdateTrustCacheBytes = 512 << 10
	maxUpdateTrustSources    = 4
	maxUpdateTrustSourceURL  = 4096
	maxUpdateTrustSourceWait = 15 * time.Second
)

type updateTrustSource struct {
	Name string
	URL  string
}

type updateTrustStore struct {
	Root           *ecdsa.PublicKey
	EmbeddedPolicy []byte
	CacheDir       string
	Client         *http.Client
	Now            func() time.Time
	Rename         func(string, string) error
	SourceTimeout  time.Duration
}

type updateTrustPolicyMode string

const (
	updateTrustModeCurrent                 updateTrustPolicyMode = "current"
	updateTrustModeExpiredIdentityFallback updateTrustPolicyMode = "expired_identity_fallback"
)

type resolvedUpdateTrustPolicy struct {
	Policy           verifiedUpdateTrustPolicy
	Mode             updateTrustPolicyMode
	FrozenIdentities []updateCertificateIdentity
	resolvedAt       time.Time
}

func (p resolvedUpdateTrustPolicy) Authorize(input updateArtifactIdentity) error {
	if p.Mode == updateTrustModeCurrent {
		return p.Policy.AuthorizeAt(input, p.resolvedAt)
	}
	if p.Mode != updateTrustModeExpiredIdentityFallback || input.Channel != updateChannelStable || !canonicalPolicyTag.MatchString(input.Tag) {
		return policyError("publisher_not_authorized")
	}
	identity := normalizeUpdateCertificateIdentity(input.Certificate)
	for _, frozen := range p.FrozenIdentities {
		if identity == frozen {
			return nil
		}
	}
	return policyError("publisher_not_authorized")
}

type persistedUpdateTrustCache struct {
	Version          int                         `json:"version"`
	HighestEpoch     uint64                      `json:"highestEpoch"`
	PolicySHA256     string                      `json:"policySha256"`
	PolicyBytes      []byte                      `json:"policyBytes"`
	FrozenIdentities []updateCertificateIdentity `json:"frozenIdentities"`
}

type loadedUpdateTrustCache struct {
	envelope persistedUpdateTrustCache
	policy   verifiedUpdateTrustPolicy
}

type updateTrustCacheState int

const (
	updateTrustCacheMissing updateTrustCacheState = iota
	updateTrustCacheValid
	updateTrustCacheCorrupt
)

func (s *updateTrustStore) Resolve(ctx context.Context, sources ...updateTrustSource) (resolvedUpdateTrustPolicy, error) {
	if len(sources) > maxUpdateTrustSources {
		return resolvedUpdateTrustPolicy{}, policyError("policy_sources_invalid")
	}
	if strings.TrimSpace(s.CacheDir) == "" {
		return resolvedUpdateTrustPolicy{}, policyError("policy_cache_unavailable")
	}
	cacheLock, err := acquireUpdateTrustCacheLock(ctx, s.CacheDir)
	if err != nil {
		return resolvedUpdateTrustPolicy{}, err
	}
	defer cacheLock.Release()
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	embedded, err := verifyUpdateTrustPolicyAtAnyExpiry(s.EmbeddedPolicy, s.Root)
	if err != nil {
		return resolvedUpdateTrustPolicy{}, policyError("policy_embedded_invalid")
	}

	cache, cacheState := s.loadCache()
	if cacheState == updateTrustCacheCorrupt {
		return resolvedUpdateTrustPolicy{}, policyError("policy_cache_corrupt")
	}
	selected := embedded
	selectedBytes := bytes.Clone(s.EmbeddedPolicy)
	selectedFromCache := false
	needsPersist := cacheState == updateTrustCacheMissing
	highestAcceptedEpoch := embedded.Epoch
	if cacheState == updateTrustCacheValid {
		if cache.envelope.HighestEpoch >= highestAcceptedEpoch {
			highestAcceptedEpoch = cache.envelope.HighestEpoch
			selected = cache.policy
			selectedBytes = bytes.Clone(cache.envelope.PolicyBytes)
			selectedFromCache = true
			needsPersist = false
		} else {
			needsPersist = true
		}
	}

	client := s.Client
	if client == nil {
		client = newUpdateHTTPClient(maxUpdateTrustSourceWait)
	}
	sourceTimeout := s.SourceTimeout
	if sourceTimeout <= 0 || sourceTimeout > maxUpdateTrustSourceWait {
		sourceTimeout = maxUpdateTrustSourceWait
	}
	for _, source := range sources {
		data, fetchErr := fetchUpdateTrustPolicy(ctx, client, source, sourceTimeout)
		if fetchErr != nil {
			if err := ctx.Err(); err != nil {
				return resolvedUpdateTrustPolicy{}, err
			}
			continue
		}
		policy, verifyErr := parseAndVerifyUpdateTrustPolicy(data, s.Root, now)
		if verifyErr != nil || policy.Epoch < highestAcceptedEpoch {
			continue
		}
		if policy.Epoch > selected.Epoch {
			selected = policy
			selectedBytes = data
			selectedFromCache = false
			needsPersist = true
		}
	}

	if !selected.ExpiresAt.After(now) {
		if selectedFromCache {
			return resolvedUpdateTrustPolicy{
				Policy:           selected,
				Mode:             updateTrustModeExpiredIdentityFallback,
				FrozenIdentities: append([]updateCertificateIdentity(nil), cache.envelope.FrozenIdentities...),
				resolvedAt:       now,
			}, nil
		}
		return resolvedUpdateTrustPolicy{}, policyError("policy_unavailable")
	}

	frozen := primaryStableLegalIdentities(selected)
	if selectedFromCache {
		frozen = append([]updateCertificateIdentity(nil), cache.envelope.FrozenIdentities...)
	}
	if needsPersist {
		if err := s.persistCache(selectedBytes, selected, frozen); err != nil {
			return resolvedUpdateTrustPolicy{}, err
		}
	}
	return resolvedUpdateTrustPolicy{Policy: selected, Mode: updateTrustModeCurrent, FrozenIdentities: frozen, resolvedAt: now}, nil
}

func updateTrustCacheLockID(cacheDir string) (string, error) {
	absolute, err := filepath.Abs(cacheDir)
	if err != nil {
		return "", policyError("policy_cache_lock_unavailable")
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		absolute = resolved
	}
	normalized := normalizeUpdateTrustCacheLockPath(absolute)
	digest := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(digest[:]), nil
}

func fetchUpdateTrustPolicy(ctx context.Context, client *http.Client, source updateTrustSource, timeout time.Duration) ([]byte, error) {
	if strings.TrimSpace(source.URL) == "" || len(source.URL) > maxUpdateTrustSourceURL {
		return nil, policyError("policy_source_invalid")
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, source.URL, nil)
	if err != nil {
		return nil, policyError("policy_source_invalid")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, policyError("policy_source_unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, policyError("policy_source_unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxUpdateTrustPolicyBytes+1))
	if err != nil {
		return nil, policyError("policy_source_unavailable")
	}
	if len(data) == 0 || len(data) > maxUpdateTrustPolicyBytes {
		return nil, policyError("policy_size_invalid")
	}
	return data, nil
}

func verifyUpdateTrustPolicyAtAnyExpiry(data []byte, root *ecdsa.PublicKey) (verifiedUpdateTrustPolicy, error) {
	return parseAndVerifyUpdateTrustPolicy(data, root, time.Time{})
}

func (s *updateTrustStore) loadCache() (loadedUpdateTrustCache, updateTrustCacheState) {
	path := filepath.Join(s.CacheDir, updateTrustCacheFilename)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return loadedUpdateTrustCache{}, updateTrustCacheMissing
	}
	if err != nil {
		return loadedUpdateTrustCache{}, updateTrustCacheCorrupt
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxUpdateTrustCacheBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxUpdateTrustCacheBytes {
		return loadedUpdateTrustCache{}, updateTrustCacheCorrupt
	}
	if err := validatePublisherPolicyJSON(data); err != nil {
		return loadedUpdateTrustCache{}, updateTrustCacheCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope persistedUpdateTrustCache
	if err := decoder.Decode(&envelope); err != nil {
		return loadedUpdateTrustCache{}, updateTrustCacheCorrupt
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return loadedUpdateTrustCache{}, updateTrustCacheCorrupt
	}
	if envelope.Version != updateTrustCacheVersion || envelope.HighestEpoch == 0 || len(envelope.PolicyBytes) == 0 || len(envelope.PolicyBytes) > maxUpdateTrustPolicyBytes || !sha256Hex.MatchString(envelope.PolicySHA256) {
		return loadedUpdateTrustCache{}, updateTrustCacheCorrupt
	}
	digest := sha256.Sum256(envelope.PolicyBytes)
	if !strings.EqualFold(envelope.PolicySHA256, hex.EncodeToString(digest[:])) {
		return loadedUpdateTrustCache{}, updateTrustCacheCorrupt
	}
	policy, err := verifyUpdateTrustPolicyAtAnyExpiry(envelope.PolicyBytes, s.Root)
	if err != nil || policy.Epoch != envelope.HighestEpoch {
		return loadedUpdateTrustCache{}, updateTrustCacheCorrupt
	}
	expectedFrozen := primaryStableLegalIdentities(policy)
	if !equalUpdateLegalIdentities(envelope.FrozenIdentities, expectedFrozen) {
		return loadedUpdateTrustCache{}, updateTrustCacheCorrupt
	}
	return loadedUpdateTrustCache{envelope: envelope, policy: policy}, updateTrustCacheValid
}

func (s *updateTrustStore) persistCache(policyBytes []byte, policy verifiedUpdateTrustPolicy, frozen []updateCertificateIdentity) error {
	if strings.TrimSpace(s.CacheDir) == "" {
		return policyError("policy_cache_unavailable")
	}
	digest := sha256.Sum256(policyBytes)
	envelope := persistedUpdateTrustCache{
		Version:          updateTrustCacheVersion,
		HighestEpoch:     policy.Epoch,
		PolicySHA256:     hex.EncodeToString(digest[:]),
		PolicyBytes:      bytes.Clone(policyBytes),
		FrozenIdentities: append([]updateCertificateIdentity(nil), frozen...),
	}
	data, err := json.Marshal(envelope)
	if err != nil || len(data) > maxUpdateTrustCacheBytes {
		return policyError("policy_cache_invalid")
	}
	if err := os.MkdirAll(s.CacheDir, 0o700); err != nil {
		return sanitizeTrustStoreError("policy_cache_create", err)
	}
	temporary, err := os.CreateTemp(s.CacheDir, "publisher-policy-cache-*.tmp")
	if err != nil {
		return sanitizeTrustStoreError("policy_cache_create", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return sanitizeTrustStoreError("policy_cache_write", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return sanitizeTrustStoreError("policy_cache_write", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return sanitizeTrustStoreError("policy_cache_sync", err)
	}
	if err := temporary.Close(); err != nil {
		return sanitizeTrustStoreError("policy_cache_close", err)
	}
	finalPath := filepath.Join(s.CacheDir, updateTrustCacheFilename)
	if s.Rename != nil {
		err = s.Rename(temporaryPath, finalPath)
	} else {
		outcome := replaceFileAtomically(temporaryPath, finalPath)
		err = outcome.Err
	}
	if err != nil {
		return sanitizeTrustStoreError("policy_cache_rename", err)
	}
	if err := syncStateDirectory(s.CacheDir); err != nil {
		return sanitizeTrustStoreError("policy_cache_directory_sync", err)
	}
	return nil
}

func primaryStableLegalIdentities(policy verifiedUpdateTrustPolicy) []updateCertificateIdentity {
	identities := make([]updateCertificateIdentity, 0, len(policy.Rules))
	for _, rule := range policy.Rules {
		if rule.Role != "primary" || rule.AllowedChannel != updateChannelStable {
			continue
		}
		identity := normalizeUpdateCertificateIdentity(updateCertificateIdentity{Country: rule.Country, Organization: rule.Organization, OrganizationID: rule.OrganizationID})
		seen := false
		for _, existing := range identities {
			if existing == identity {
				seen = true
				break
			}
		}
		if !seen {
			identities = append(identities, identity)
		}
	}
	return identities
}

func equalUpdateLegalIdentities(left, right []updateCertificateIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if normalizeUpdateCertificateIdentity(left[index]) != right[index] {
			return false
		}
	}
	return true
}

func sanitizeTrustStoreError(code string, err error) error {
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		err = pathError.Err
	}
	return fmt.Errorf("%s: %w", code, err)
}
