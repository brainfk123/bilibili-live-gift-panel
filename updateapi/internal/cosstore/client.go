package cosstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	cos "github.com/tencentyun/cos-go-sdk-v5"
)

const (
	defaultTimeout   = 10 * time.Second
	presignTTL       = 10 * time.Minute
	stableChannelKey = "channels/stable/latest.json"
)

// ErrNotFound indicates that COS did not contain the requested object.
var ErrNotFound = errors.New("COS object not found")

// ErrAlreadyExists indicates that COS rejected an atomic no-overwrite upload.
var ErrAlreadyExists = errors.New("COS object already exists")

var (
	cosBucketPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,48}[a-z0-9])?-[1-9][0-9]{4,19}$`)
	cosRegionPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}[a-z0-9]$`)
)

// ObjectInfo is the immutable-object metadata used to verify COS uploads.
type ObjectInfo struct {
	Size   int64
	SHA256 string
	ETag   string
}

type Client struct {
	client    *cos.Client
	secretID  string
	secretKey string
}

func New(bucket, region, secretID, secretKey string, httpClient *http.Client) (*Client, error) {
	if bucket == "" || region == "" || secretID == "" || secretKey == "" {
		return nil, errors.New("bucket, region, secret ID, and secret key are required")
	}
	if len(bucket) > 63 || !cosBucketPattern.MatchString(bucket) {
		return nil, errors.New("COS bucket name is invalid")
	}
	if !cosRegionPattern.MatchString(region) {
		return nil, errors.New("COS region is invalid")
	}

	expectedHost := fmt.Sprintf("%s.cos.%s.myqcloud.com", bucket, region)
	bucketURL := &url.URL{Scheme: "https", Host: expectedHost}
	if bucketURL.Scheme != "https" || bucketURL.Host != expectedHost || bucketURL.User != nil || bucketURL.Path != "" || bucketURL.RawQuery != "" || bucketURL.Fragment != "" {
		return nil, errors.New("COS bucket URL is invalid")
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
		return nil, "", normalizeNotFound(err)
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

// Head reads object metadata without downloading the object body.
func (client *Client) Head(ctx context.Context, key string) (ObjectInfo, error) {
	if !isAllowedReadKey(key) {
		return ObjectInfo{}, fmt.Errorf("object key %q is outside allowed paths", key)
	}
	response, err := client.client.Object.Head(ctx, key, nil)
	if err != nil {
		return ObjectInfo{}, normalizeNotFound(err)
	}
	return ObjectInfo{
		Size:   response.ContentLength,
		SHA256: response.Header.Get("X-Cos-Meta-Sha256"),
		ETag:   response.Header.Get("ETag"),
	}, nil
}

// Put replaces the stable channel pointer.
func (client *Client) Put(ctx context.Context, key string, body io.Reader, size int64, contentType, sha256 string) error {
	if key != stableChannelKey {
		return fmt.Errorf("replaceable write key must be %q", stableChannelKey)
	}
	return client.put(ctx, key, body, size, contentType, sha256, false)
}

// PutImmutable creates a versioned object without allowing an existing object
// to be overwritten by a concurrent publisher.
func (client *Client) PutImmutable(ctx context.Context, key string, body io.Reader, size int64, contentType, sha256 string) error {
	if !isReleaseKey(key) {
		return fmt.Errorf("immutable write key %q is outside releases/", key)
	}
	return client.put(ctx, key, body, size, contentType, sha256, true)
}

func (client *Client) put(ctx context.Context, key string, body io.Reader, size int64, contentType, sha256 string, forbidOverwrite bool) error {
	if body == nil || size < 0 {
		return errors.New("object body and non-negative size are required")
	}
	metadata := make(http.Header)
	metadata.Set("x-cos-meta-sha256", sha256)
	options := make(http.Header)
	if forbidOverwrite {
		options.Set("x-cos-forbid-overwrite", "true")
	}
	_, err := client.client.Object.Put(ctx, key, body, &cos.ObjectPutOptions{
		ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{
			ContentLength: size,
			ContentType:   contentType,
			XCosMetaXXX:   &metadata,
			XOptionHeader: &options,
		},
	})
	return normalizeCOSError(err)
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

func normalizeNotFound(err error) error {
	return normalizeCOSError(err)
}

func normalizeCOSError(err error) error {
	if err == nil {
		return nil
	}
	if response, ok := cos.IsCOSError(err); ok {
		if response.Code == "FileAlreadyExists" {
			return fmt.Errorf("%w: %v", ErrAlreadyExists, err)
		}
		if response.Response != nil && response.Response.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %v", ErrNotFound, err)
		}
	}
	return err
}
