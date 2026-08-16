package mirror

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type staticResolver map[string][]net.IPAddr

func (resolver staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	addresses, ok := resolver[host]
	if !ok {
		return nil, errors.New("unexpected hostname")
	}
	return addresses, nil
}

func testIP(value string) net.IP {
	address := netip.MustParseAddr(value)
	return net.IP(address.AsSlice())
}

func TestRestrictedTransportRejectsNonHTTPSAndHostsOutsideExactAllowlist(t *testing.T) {
	client := newRestrictedClient(staticResolver{}, func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("dial must not run for a rejected request")
		return nil, nil
	})

	for _, rawURL := range []string{
		"http://github.com/file",
		"https://github.com.evil.example/file",
		"https://user@github.com/file",
		"https://api.github.com:444/file",
	} {
		request, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Do(request); err == nil {
			t.Fatalf("Do(%q) error = nil, want rejection", rawURL)
		}
	}
}

func TestRestrictedTransportAllowsOnlyProductionHostsAndPinsDNSAtDial(t *testing.T) {
	const publicIP = "8.8.8.8"
	dialFailure := errors.New("dial stopped by test")
	for _, host := range []string{
		"api.github.com",
		"github.com",
		"release-assets.githubusercontent.com",
		"objects.githubusercontent.com",
		"github-releases.githubusercontent.com",
	} {
		t.Run(host, func(t *testing.T) {
			var dialAddress string
			client := newRestrictedClient(
				staticResolver{host: {{IP: testIP(publicIP)}}},
				func(_ context.Context, network, address string) (net.Conn, error) {
					if network != "tcp" {
						t.Fatalf("network = %q, want tcp", network)
					}
					dialAddress = address
					return nil, dialFailure
				},
			)
			request, err := http.NewRequest(http.MethodGet, "https://"+host+"/asset", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Do(request)
			if !errors.Is(err, dialFailure) {
				t.Fatalf("Do() error = %v, want injected dial failure", err)
			}
			if dialAddress != publicIP+":443" {
				t.Fatalf("dial address = %q, want validated IP literal", dialAddress)
			}
		})
	}
}

func TestRestrictedTransportRejectsNonPublicResolvedAddresses(t *testing.T) {
	addresses := []string{
		"127.0.0.1",       // loopback
		"0.0.0.0",         // unspecified
		"0.0.0.1",         // this-network block
		"224.0.0.1",       // multicast
		"169.254.1.1",     // link local
		"10.0.0.1",        // RFC1918
		"172.16.0.1",      // RFC1918
		"192.168.0.1",     // RFC1918
		"100.64.0.1",      // carrier-grade NAT
		"192.0.2.1",       // documentation
		"198.51.100.1",    // documentation
		"203.0.113.1",     // documentation
		"198.18.0.1",      // benchmarking
		"192.0.0.1",       // IETF protocol assignment
		"240.0.0.1",       // reserved
		"255.255.255.255", // limited broadcast
		"::1",             // loopback
		"::",              // unspecified
		"ff02::1",         // multicast
		"fe80::1",         // link local
		"fd00::1",         // unique local
		"2001:db8::1",     // documentation
		"2001::1",         // IETF protocol assignment
		"3fff::1",         // documentation
		"5f00::1",         // segment-routing local block
		"::ffff:8.8.8.8",  // IPv4-mapped IPv6
	}

	for _, address := range addresses {
		t.Run(strings.ReplaceAll(address, ":", "_"), func(t *testing.T) {
			dialed := false
			client := newRestrictedClient(
				staticResolver{"github.com": {{IP: testIP(address)}}},
				func(context.Context, string, string) (net.Conn, error) {
					dialed = true
					return nil, errors.New("must not dial")
				},
			)
			request, err := http.NewRequest(http.MethodGet, "https://github.com/asset", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Do(request); err == nil {
				t.Fatal("Do() error = nil, want non-public address rejection")
			}
			if dialed {
				t.Fatal("dialer called for rejected address")
			}
		})
	}
}

func TestRestrictedTransportSetsBoundedTimeoutsAndNeverUsesProxyEnvironment(t *testing.T) {
	client := newRestrictedClient(staticResolver{}, func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unused")
	})
	restricted, ok := client.Transport.(*restrictedRoundTripper)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	transport := restricted.transport
	if transport.Proxy != nil {
		t.Fatal("transport consults proxy configuration")
	}
	if transport.DialContext == nil || transport.TLSHandshakeTimeout <= 0 || transport.ResponseHeaderTimeout <= 0 {
		t.Fatalf("timeouts are not bounded: dial=%v TLS=%s headers=%s", transport.DialContext != nil, transport.TLSHandshakeTimeout, transport.ResponseHeaderTimeout)
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.ServerName != "" {
		t.Fatal("TLS config must derive SNI and certificate hostname from each original request")
	}
}

func TestRestrictedTransportRetainsOriginalHostnameForTLSVerification(t *testing.T) {
	certificate, roots := githubTestCertificate(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.TLS == nil || request.TLS.ServerName != "github.com" {
			t.Errorf("TLS SNI = %q, want github.com", request.TLS.ServerName)
		}
		_, _ = writer.Write([]byte("ok"))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	defer server.Close()

	dialer := &net.Dialer{Timeout: time.Second}
	client := newRestrictedClient(
		staticResolver{"github.com": {{IP: testIP("8.8.8.8")}}},
		func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:443" {
				t.Fatalf("validated dial address = %q", address)
			}
			return dialer.DialContext(ctx, network, server.Listener.Addr().String())
		},
	)
	client.Transport.(*restrictedRoundTripper).transport.TLSClientConfig.RootCAs = roots
	response, err := client.Get("https://github.com/asset")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func githubTestCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "github.com"},
		DNSNames:     []string{"github.com"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustMarshalPKCS8(t, privateKey)}),
	)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(parsed)
	return certificate, roots
}

func mustMarshalPKCS8(t *testing.T, key any) []byte {
	t.Helper()
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestRedirectPolicyRevalidatesEveryHopAndCapsRedirects(t *testing.T) {
	client := newRestrictedClient(staticResolver{}, func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unused")
	})

	allowed, _ := http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/file", nil)
	previous, _ := http.NewRequest(http.MethodGet, "https://github.com/file", nil)
	if err := client.CheckRedirect(allowed, []*http.Request{previous}); err != nil {
		t.Fatalf("allowed redirect rejected: %v", err)
	}

	for _, rawURL := range []string{
		"http://release-assets.githubusercontent.com/file",
		"https://release-assets.githubusercontent.com.evil.example/file",
	} {
		next, _ := http.NewRequest(http.MethodGet, rawURL, nil)
		if err := client.CheckRedirect(next, []*http.Request{previous}); err == nil {
			t.Fatalf("redirect to %q accepted", rawURL)
		}
	}

	via := make([]*http.Request, maxRedirects)
	for index := range via {
		via[index] = previous
	}
	if err := client.CheckRedirect(allowed, via); err == nil {
		t.Fatal("redirect limit accepted one hop too many")
	}
}
