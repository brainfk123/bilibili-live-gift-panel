package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const updateRedirectLimit = 5

var blockedUpdateAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func newUpdateHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = safeUpdateDialContext
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     safeUpdateTransport{base: transport},
		CheckRedirect: checkUpdateRedirect,
	}
}

type safeUpdateTransport struct {
	base http.RoundTripper
}

func (transport safeUpdateTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := validateUpdateURL(request.URL); err != nil {
		return nil, err
	}
	return transport.base.RoundTrip(request)
}

func checkUpdateRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= updateRedirectLimit {
		return errors.New("更新下载重定向次数过多")
	}
	return validateUpdateURL(request.URL)
}

func validateUpdateURL(target *url.URL) error {
	if target == nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.Fragment != "" {
		return errors.New("更新地址不安全")
	}
	hostname := strings.TrimSuffix(strings.ToLower(target.Hostname()), ".")
	if hostname == "" || hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return errors.New("更新地址不安全")
	}
	if parsed := net.ParseIP(hostname); parsed != nil && !updateAddressIsPublic(parsed) {
		return errors.New("更新地址不安全")
	}
	return nil
}

func updateAddressIsPublic(ip net.IP) bool {
	if ip == nil {
		return false
	}
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() || address.IsMulticast() {
		return false
	}
	for _, prefix := range blockedUpdateAddressPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func safeUpdateDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("更新地址无效")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, safeUpdateNetworkError(err)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	for _, resolved := range addresses {
		if !updateAddressIsPublic(resolved.IP) {
			continue
		}
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
		if dialErr == nil {
			return connection, nil
		}
	}
	return nil, errors.New("更新地址未解析到可用公网地址")
}

func safeUpdateNetworkError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("网络请求已取消")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("网络请求超时")
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("网络请求超时")
	}
	return errors.New("网络请求失败")
}
