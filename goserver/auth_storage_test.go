package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFileLoginCredentialStoreEncryptsSensitiveCookies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.dat")
	reverse := func(value []byte) ([]byte, error) {
		copy := append([]byte(nil), value...)
		for left, right := 0, len(copy)-1; left < right; left, right = left+1, right-1 {
			copy[left], copy[right] = copy[right], copy[left]
		}
		return copy, nil
	}
	store := newFileLoginCredentialStore(path, reverse, reverse)
	want := loginCredentials{
		UID: 32249588, Uname: "反重力鱼", Avatar: "face.jpg",
		Cookies:      map[string]string{"SESSDATA": "secret-session", "bili_jct": "csrf-secret"},
		RefreshToken: "refresh-secret",
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte("secret-session"), []byte("csrf-secret"), []byte("refresh-secret")} {
		if bytes.Contains(raw, secret) {
			t.Fatalf("credential file contains plaintext secret %q", secret)
		}
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.UID != want.UID || got.Cookies["SESSDATA"] != want.Cookies["SESSDATA"] || got.RefreshToken != want.RefreshToken {
		t.Fatalf("loaded credentials = %#v", got)
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != errLoginCredentialsNotFound {
		t.Fatalf("load after delete error = %v", err)
	}
}
