//go:build !windows

package main

import "errors"

func verifyAuthenticodePublisher(string, string) error {
	return errors.New("当前系统不支持 Authenticode 发布者验证")
}

func inspectAuthenticode(string) (inspectedUpdateCertificate, error) {
	return inspectedUpdateCertificate{}, errors.New("当前系统不支持 Authenticode 证书检查")
}
