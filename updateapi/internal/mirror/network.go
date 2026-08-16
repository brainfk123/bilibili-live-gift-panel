package mirror

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	connectTimeout        = 10 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 15 * time.Second
	maxRedirects          = 5
)

var restrictedHosts = map[string]struct{}{
	"api.github.com":                        {},
	"github.com":                            {},
	"release-assets.githubusercontent.com":  {},
	"objects.githubusercontent.com":         {},
	"github-releases.githubusercontent.com": {},
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("3fff::/20"),
}

var globalIPv6Prefix = netip.MustParsePrefix("2000::/3")

type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

type restrictedRoundTripper struct {
	transport *http.Transport
}

func (transport *restrictedRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := validateRestrictedURL(request.URL); err != nil {
		return nil, err
	}
	return transport.transport.RoundTrip(request)
}

// NewRestrictedHTTPClient returns a client limited to the public GitHub hosts
// needed by the release mirror. It deliberately ignores proxy environment
// variables so host validation and the validated dial address cannot diverge.
func NewRestrictedHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	return newRestrictedClient(net.DefaultResolver, dialer.DialContext)
}

func newRestrictedClient(resolver ipResolver, dial dialContextFunc) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           restrictedDialContext(resolver, dial),
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport:     &restrictedRoundTripper{transport: transport},
		CheckRedirect: restrictedRedirectPolicy,
	}
}

func restrictedRedirectPolicy(request *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("GitHub download exceeded redirect limit")
	}
	return validateRestrictedURL(request.URL)
}

func validateRestrictedURL(target *url.URL) error {
	if target == nil || target.Scheme != "https" || target.User != nil {
		return errors.New("GitHub request URL is not allowed")
	}
	host := strings.ToLower(target.Hostname())
	if _, allowed := restrictedHosts[host]; !allowed {
		return errors.New("GitHub request host is not allowed")
	}
	if port := target.Port(); port != "" && port != "443" {
		return errors.New("GitHub request port is not allowed")
	}
	return nil
}

func restrictedDialContext(resolver ipResolver, dial dialContextFunc) dialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("could not parse GitHub dial address")
		}
		if _, allowed := restrictedHosts[strings.ToLower(host)]; !allowed {
			return nil, errors.New("GitHub dial host is not allowed")
		}
		if parsedPort, err := strconv.Atoi(port); err != nil || parsedPort != 443 {
			return nil, errors.New("GitHub dial port is not allowed")
		}

		addresses, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, errors.New("could not resolve GitHub host")
		}
		for _, resolved := range addresses {
			ip, ok := netip.AddrFromSlice(resolved.IP)
			if !ok || !isPublicIP(ip) {
				continue
			}
			return dial(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		return nil, errors.New("GitHub host did not resolve to a public address")
	}
}

func isPublicIP(address netip.Addr) bool {
	if !address.IsValid() || address.Is4In6() || !address.IsGlobalUnicast() ||
		address.IsPrivate() || address.IsLoopback() || address.IsUnspecified() ||
		address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return false
	}
	if address.Is6() && !globalIPv6Prefix.Contains(address) {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
