package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type credentialProtector func([]byte) ([]byte, error)

type fileLoginCredentialStore struct {
	path      string
	protect   credentialProtector
	unprotect credentialProtector
	mu        sync.Mutex
}

func newDefaultLoginCredentialStore() (*fileLoginCredentialStore, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("无法确定登录凭证目录：%w", err)
	}
	return newFileLoginCredentialStore(
		filepath.Join(root, "BilibiliLiveGiftPanel", "auth.dat"),
		protectLoginCredentialData,
		unprotectLoginCredentialData,
	), nil
}

func newFileLoginCredentialStore(path string, protect, unprotect credentialProtector) *fileLoginCredentialStore {
	return &fileLoginCredentialStore{path: path, protect: protect, unprotect: unprotect}
}

func (store *fileLoginCredentialStore) Load() (loginCredentials, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	encrypted, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return loginCredentials{}, errLoginCredentialsNotFound
	}
	if err != nil {
		return loginCredentials{}, fmt.Errorf("读取登录凭证失败：%w", err)
	}
	plain, err := store.unprotect(encrypted)
	if err != nil {
		return loginCredentials{}, fmt.Errorf("解密登录凭证失败：%w", err)
	}
	var credentials loginCredentials
	if err := json.Unmarshal(plain, &credentials); err != nil {
		return loginCredentials{}, fmt.Errorf("解析登录凭证失败：%w", err)
	}
	if credentials.Cookies == nil {
		credentials.Cookies = map[string]string{}
	}
	return credentials, nil
}

func (store *fileLoginCredentialStore) Save(credentials loginCredentials) error {
	plain, err := json.Marshal(credentials)
	if err != nil {
		return fmt.Errorf("序列化登录凭证失败：%w", err)
	}
	encrypted, err := store.protect(plain)
	if err != nil {
		return fmt.Errorf("加密登录凭证失败：%w", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := writeFileAtomically(store.path, encrypted); err != nil {
		return fmt.Errorf("写入登录凭证失败：%w", err)
	}
	return nil
}

func (store *fileLoginCredentialStore) Delete() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	err := os.Remove(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return errLoginCredentialsNotFound
	}
	return err
}
