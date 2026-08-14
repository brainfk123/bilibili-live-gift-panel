package cosstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	cos "github.com/tencentyun/cos-go-sdk-v5"
)

const (
	defaultTimeout   = 10 * time.Second
	presignTTL       = 10 * time.Minute
	stableChannelKey = "channels/stable/latest.json"
)

type Client struct {
	client    *cos.Client
	secretID  string
	secretKey string
}

func New(bucket, region, secretID, secretKey string, httpClient *http.Client) (*Client, error) {
	if bucket == "" || region == "" || secretID == "" || secretKey == "" {
		return nil, errors.New("bucket, region, secret ID, and secret key are required")
	}

	bucketURL, err := url.Parse(fmt.Sprintf("https://%s.cos.%s.myqcloud.com", bucket, region))
	if err != nil {
		return nil, fmt.Errorf("parse COS bucket URL: %w", err)
	}

	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	configuredClient := *httpClient
	configuredClient.Transport = &cos.AuthorizationTransport{
		SecretID:  secretID,
		SecretKey: secretKey,
		Transport: httpClient.Transport,
	}

	return &Client{
		client:    cos.NewClient(&cos.BaseURL{BucketURL: bucketURL}, &configuredClient),
		secretID:  secretID,
		secretKey: secretKey,
	}, nil
}

func (client *Client) Get(ctx context.Context, key string, maxBytes int64) ([]byte, string, error) {
	if maxBytes < 0 {
		return nil, "", errors.New("maximum object size must not be negative")
	}
	if !isAllowedReadKey(key) {
		return nil, "", fmt.Errorf("object key %q is outside allowed read paths", key)
	}

	response, err := client.client.Object.Get(ctx, key, nil)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > maxBytes {
		return nil, "", fmt.Errorf("object %q exceeds %d byte limit", key, maxBytes)
	}
	return body, response.Header.Get("ETag"), nil
}

func (client *Client) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if !isReleaseKey(key) {
		return "", fmt.Errorf("object key %q is outside releases/", key)
	}
	if ttl != presignTTL {
		return "", fmt.Errorf("presigned URL TTL must be %s", presignTTL)
	}

	signedURL, err := client.client.Object.GetPresignedURL(ctx, http.MethodGet, key, client.secretID, client.secretKey, ttl, nil)
	if err != nil {
		return "", err
	}
	return signedURL.String(), nil
}

func isAllowedReadKey(key string) bool {
	return key == stableChannelKey || isReleaseKey(key)
}

func isReleaseKey(key string) bool {
	if !strings.HasPrefix(key, "releases/") || key == "releases/" {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
