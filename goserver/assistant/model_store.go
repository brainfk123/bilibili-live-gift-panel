package assistant

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maximumModelBytes int64 = 1 << 30

var safeModelVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)

type SignedManifest struct {
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
}

type ModelManifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	ModelID       string `json:"modelId"`
	Version       string `json:"version"`
	Repository    string `json:"repository"`
	Revision      string `json:"revision"`
	File          string `json:"file"`
	DownloadURL   string `json:"downloadUrl,omitempty"`
	SizeBytes     int64  `json:"sizeBytes"`
	SHA256        string `json:"sha256"`
	Architecture  string `json:"architecture"`
	Quantization  string `json:"quantization"`
	MinAppVersion string `json:"minAppVersion,omitempty"`
	ReleaseNotes  string `json:"releaseNotes,omitempty"`
}

type activeModel struct {
	Manifest ModelManifest `json:"manifest"`
	Path     string        `json:"path"`
}

type DownloadProgress func(installed, total int64)

type ModelStoreOptions struct {
	Root          string
	ManifestURL   string
	PublicKey     ed25519.PublicKey
	HTTPClient    *http.Client
	AllowedHosts  []string
	AllowHTTPTest bool
}

type ModelStore struct {
	root          string
	manifestURL   string
	publicKey     ed25519.PublicKey
	client        *http.Client
	allowedHosts  []string
	allowHTTPTest bool
}

func DecodePublicKeyBase64(value string) (ed25519.PublicKey, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("未配置答疑模型清单公钥")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(data) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("答疑模型清单公钥格式无效")
	}
	return ed25519.PublicKey(data), nil
}

func (store *ModelStore) ConfigurationError() error {
	if store.manifestURL == "" {
		return fmt.Errorf("未配置答疑模型清单地址")
	}
	if len(store.publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("未配置有效的答疑模型清单公钥，请安装正式发布版本")
	}
	return store.validateRemoteURL(store.manifestURL)
}

func NewModelStore(options ModelStoreOptions) (*ModelStore, error) {
	if strings.TrimSpace(options.Root) == "" {
		return nil, fmt.Errorf("模型目录不能为空")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	hosts := options.AllowedHosts
	if len(hosts) == 0 {
		hosts = []string{
			"modelscope.cn",
			"modelscope.oss-cn-beijing.aliyuncs.com",
			"modelscope.oss-cn-hangzhou.aliyuncs.com",
			"modelscope.oss-cn-shanghai.aliyuncs.com",
		}
	}
	return &ModelStore{
		root: options.Root, manifestURL: strings.TrimSpace(options.ManifestURL),
		publicKey: append(ed25519.PublicKey(nil), options.PublicKey...), client: client,
		allowedHosts: hosts, allowHTTPTest: options.AllowHTTPTest,
	}, nil
}

func (store *ModelStore) FetchManifest(ctx context.Context) (ModelManifest, error) {
	if store.manifestURL == "" {
		return ModelManifest{}, fmt.Errorf("未配置答疑模型清单地址")
	}
	if len(store.publicKey) != ed25519.PublicKeySize {
		return ModelManifest{}, fmt.Errorf("未配置有效的答疑模型清单公钥")
	}
	if err := store.validateRemoteURL(store.manifestURL); err != nil {
		return ModelManifest{}, fmt.Errorf("模型清单地址不安全：%w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, store.manifestURL, nil)
	if err != nil {
		return ModelManifest{}, err
	}
	response, err := store.do(request)
	if err != nil {
		return ModelManifest{}, fmt.Errorf("获取模型清单失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ModelManifest{}, fmt.Errorf("获取模型清单失败：HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 256<<10))
	if err != nil {
		return ModelManifest{}, fmt.Errorf("读取模型清单失败：%w", err)
	}
	var envelope SignedManifest
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope.Payload) == 0 {
		return ModelManifest{}, fmt.Errorf("模型清单格式无效")
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(store.publicKey, envelope.Payload, signature) {
		return ModelManifest{}, fmt.Errorf("模型清单签名无效")
	}
	var manifest ModelManifest
	if err := json.Unmarshal(envelope.Payload, &manifest); err != nil {
		return ModelManifest{}, fmt.Errorf("模型清单内容无效：%w", err)
	}
	if err := store.validateManifest(manifest); err != nil {
		return ModelManifest{}, err
	}
	return manifest, nil
}

func (store *ModelStore) validateManifest(manifest ModelManifest) error {
	if manifest.SchemaVersion != 1 {
		return fmt.Errorf("不支持的模型清单版本 %d", manifest.SchemaVersion)
	}
	if !safeModelVersion.MatchString(manifest.Version) {
		return fmt.Errorf("模型版本无效")
	}
	if strings.TrimSpace(manifest.ModelID) == "" || strings.TrimSpace(manifest.Repository) == "" ||
		strings.TrimSpace(manifest.Revision) == "" || strings.TrimSpace(manifest.File) == "" {
		return fmt.Errorf("模型清单缺少制品标识")
	}
	switch strings.ToLower(strings.TrimSpace(manifest.Revision)) {
	case "main", "master", "latest", "head":
		return fmt.Errorf("模型 revision 必须固定，不能使用浮动分支")
	}
	if filepath.Base(manifest.File) != manifest.File || strings.ContainsAny(manifest.File, `/\`) {
		return fmt.Errorf("模型文件名无效")
	}
	if manifest.SizeBytes <= 0 || manifest.SizeBytes > maximumModelBytes {
		return fmt.Errorf("模型大小超出 1GiB 安全限制")
	}
	if len(manifest.SHA256) != sha256.Size*2 {
		return fmt.Errorf("模型 SHA-256 无效")
	}
	if _, err := hex.DecodeString(manifest.SHA256); err != nil {
		return fmt.Errorf("模型 SHA-256 无效")
	}
	if !strings.EqualFold(manifest.Architecture, "qwen3") || !strings.EqualFold(manifest.Quantization, "Q8_0") {
		return fmt.Errorf("仅支持 Qwen3 Q8_0 模型")
	}
	if err := store.validateRemoteURL(store.downloadURL(manifest)); err != nil {
		return fmt.Errorf("模型下载地址不安全：%w", err)
	}
	return nil
}

func (store *ModelStore) downloadURL(manifest ModelManifest) string {
	if strings.TrimSpace(manifest.DownloadURL) != "" {
		return manifest.DownloadURL
	}
	segments := strings.Split(strings.Trim(manifest.Repository, "/"), "/")
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	return "https://modelscope.cn/models/" + strings.Join(segments, "/") + "/resolve/" +
		url.PathEscape(manifest.Revision) + "/" + url.PathEscape(manifest.File)
}

func (store *ModelStore) Prepare(ctx context.Context, manifest ModelManifest, progress DownloadProgress) (string, error) {
	if err := store.validateManifest(manifest); err != nil {
		return "", err
	}
	directory := filepath.Join(store.root, "models", manifest.Version)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("创建模型目录失败：%w", err)
	}
	finalPath := filepath.Join(directory, "model.gguf")
	if info, err := os.Stat(finalPath); err == nil && info.Size() == manifest.SizeBytes {
		if err := validateModelFile(finalPath, manifest); err == nil {
			if progress != nil {
				progress(manifest.SizeBytes, manifest.SizeBytes)
			}
			return finalPath, nil
		}
	}
	partialPath := finalPath + ".partial"
	installed := int64(0)
	if info, err := os.Stat(partialPath); err == nil {
		installed = info.Size()
		if installed > manifest.SizeBytes {
			installed = 0
			_ = os.Remove(partialPath)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, store.downloadURL(manifest), nil)
	if err != nil {
		return "", err
	}
	if installed > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", installed))
	}
	response, err := store.do(request)
	if err != nil {
		return "", fmt.Errorf("下载模型失败：%w", err)
	}
	defer response.Body.Close()
	appendPartial := installed > 0 && response.StatusCode == http.StatusPartialContent
	if appendPartial && !contentRangeStartsAt(response.Header.Get("Content-Range"), installed) {
		response.Body.Close()
		installed = 0
		request, err = http.NewRequestWithContext(ctx, http.MethodGet, store.downloadURL(manifest), nil)
		if err != nil {
			return "", err
		}
		response, err = store.do(request)
		if err != nil {
			return "", fmt.Errorf("重新下载模型失败：%w", err)
		}
		defer response.Body.Close()
		appendPartial = false
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return "", fmt.Errorf("下载模型失败：HTTP %d", response.StatusCode)
	}
	if !appendPartial {
		installed = 0
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendPartial {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(partialPath, flags, 0o600)
	if err != nil {
		return "", fmt.Errorf("写入模型失败：%w", err)
	}
	reader := io.LimitReader(response.Body, manifest.SizeBytes-installed+1)
	buffer := make([]byte, 256<<10)
	lastProgress := time.Now().Add(-time.Second)
	for {
		count, readErr := reader.Read(buffer)
		if count > 0 {
			if installed+int64(count) > manifest.SizeBytes {
				_ = file.Close()
				return "", fmt.Errorf("下载的模型超过清单大小")
			}
			if _, err := file.Write(buffer[:count]); err != nil {
				_ = file.Close()
				return "", fmt.Errorf("写入模型失败：%w", err)
			}
			installed += int64(count)
			if progress != nil && time.Since(lastProgress) >= 100*time.Millisecond {
				progress(installed, manifest.SizeBytes)
				lastProgress = time.Now()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			_ = file.Close()
			return "", fmt.Errorf("读取模型下载失败：%w", readErr)
		}
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("同步模型文件失败：%w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("关闭模型文件失败：%w", err)
	}
	if installed != manifest.SizeBytes {
		return "", fmt.Errorf("模型下载不完整：%d/%d", installed, manifest.SizeBytes)
	}
	if err := validateModelFile(partialPath, manifest); err != nil {
		_ = os.Remove(partialPath)
		return "", err
	}
	backupPath := finalPath + ".previous"
	_ = os.Remove(backupPath)
	if _, err := os.Stat(finalPath); err == nil {
		if err := os.Rename(finalPath, backupPath); err != nil {
			return "", fmt.Errorf("保留现有模型失败：%w", err)
		}
	}
	if err := os.Rename(partialPath, finalPath); err != nil {
		_ = os.Rename(backupPath, finalPath)
		return "", fmt.Errorf("完成模型下载失败：%w", err)
	}
	_ = os.Remove(backupPath)
	if progress != nil {
		progress(manifest.SizeBytes, manifest.SizeBytes)
	}
	return finalPath, nil
}

func (store *ModelStore) Activate(manifest ModelManifest, modelPath string) error {
	relative, err := filepath.Rel(store.root, modelPath)
	if err != nil || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		return fmt.Errorf("模型路径不在答疑模型目录内")
	}
	record := activeModel{Manifest: manifest, Path: relative}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(store.root, "active-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	activePath := filepath.Join(store.root, "active.json")
	backupPath := filepath.Join(store.root, "active.previous.json")
	_ = os.Remove(backupPath)
	if _, err := os.Stat(activePath); err == nil {
		if err := os.Rename(activePath, backupPath); err != nil {
			return fmt.Errorf("保留当前模型状态失败：%w", err)
		}
	}
	if err := os.Rename(temporaryPath, activePath); err != nil {
		_ = os.Rename(backupPath, activePath)
		return fmt.Errorf("激活模型失败：%w", err)
	}
	_ = os.Remove(backupPath)
	return nil
}

func (store *ModelStore) Active() (ModelManifest, string, error) {
	activePath := filepath.Join(store.root, "active.json")
	data, err := os.ReadFile(activePath)
	if errors.Is(err, os.ErrNotExist) {
		backupPath := filepath.Join(store.root, "active.previous.json")
		if backup, backupErr := os.ReadFile(backupPath); backupErr == nil {
			if renameErr := os.Rename(backupPath, activePath); renameErr == nil {
				data, err = backup, nil
			}
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return ModelManifest{}, "", nil
	}
	if err != nil {
		return ModelManifest{}, "", err
	}
	var record activeModel
	if err := json.Unmarshal(data, &record); err != nil {
		return ModelManifest{}, "", fmt.Errorf("读取当前模型失败：%w", err)
	}
	path := filepath.Join(store.root, record.Path)
	relative, err := filepath.Rel(store.root, path)
	if err != nil || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		return ModelManifest{}, "", fmt.Errorf("当前模型路径无效")
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		backupPath := path + ".previous"
		if backupInfo, backupErr := os.Stat(backupPath); backupErr == nil && backupInfo.Size() == record.Manifest.SizeBytes {
			if renameErr := os.Rename(backupPath, path); renameErr == nil {
				info, statErr = backupInfo, nil
			}
		}
	}
	if statErr != nil || info.Size() != record.Manifest.SizeBytes {
		return ModelManifest{}, "", fmt.Errorf("当前模型文件缺失或大小无效")
	}
	return record.Manifest, path, nil
}

func (store *ModelStore) Validate(path string, manifest ModelManifest) error {
	return validateModelFile(path, manifest)
}

func contentRangeStartsAt(value string, expected int64) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(value), "bytes ") {
		return false
	}
	rangeAndTotal := strings.SplitN(strings.TrimSpace(value[6:]), "/", 2)
	if len(rangeAndTotal) != 2 {
		return false
	}
	startAndEnd := strings.SplitN(rangeAndTotal[0], "-", 2)
	if len(startAndEnd) != 2 {
		return false
	}
	start, err := strconv.ParseInt(startAndEnd[0], 10, 64)
	return err == nil && start == expected
}

func (store *ModelStore) DeleteAll() error {
	models := filepath.Join(store.root, "models")
	relative, err := filepath.Rel(store.root, models)
	if err != nil || relative != "models" {
		return fmt.Errorf("拒绝删除不安全的模型路径")
	}
	if err := os.RemoveAll(models); err != nil {
		return fmt.Errorf("删除模型失败：%w", err)
	}
	if err := os.Remove(filepath.Join(store.root, "active.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除模型状态失败：%w", err)
	}
	if err := os.Remove(filepath.Join(store.root, "active.previous.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除模型备份状态失败：%w", err)
	}
	return nil
}

func (store *ModelStore) validateRemoteURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("URL 无效")
	}
	if parsed.Scheme != "https" && !(store.allowHTTPTest && parsed.Scheme == "http") {
		return fmt.Errorf("必须使用 HTTPS")
	}
	host := strings.ToLower(parsed.Hostname())
	for _, allowed := range store.allowedHosts {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return nil
		}
	}
	return fmt.Errorf("主机 %s 不在允许列表", host)
}

func (store *ModelStore) do(request *http.Request) (*http.Response, error) {
	client := *store.client
	originalCheck := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 10 {
			return fmt.Errorf("重定向次数过多")
		}
		if err := store.validateRemoteURL(request.URL.String()); err != nil {
			return err
		}
		if originalCheck != nil {
			return originalCheck(request, via)
		}
		return nil
	}
	return client.Do(request)
}
