//go:build !windows

package main

import "fmt"

func protectLoginCredentialData([]byte) ([]byte, error) {
	return nil, fmt.Errorf("当前系统不支持登录凭证加密")
}

func unprotectLoginCredentialData([]byte) ([]byte, error) {
	return nil, fmt.Errorf("当前系统不支持登录凭证解密")
}
