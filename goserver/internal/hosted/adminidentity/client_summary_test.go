package adminidentity

import (
	"net"
	"strings"
	"testing"
)

func TestSummarizeClientAllowlistAndNetworkMasking(t *testing.T) {
	iphoneSafari := "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1"
	windowsEdge := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0"
	androidChrome := "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 Chrome/131.0.0.0 Mobile Safari/537.36"

	tests := []struct {
		name    string
		ua      string
		ip      string
		label   string
		network string
	}{
		{"iPhone Safari IPv4", iphoneSafari, "203.0.113.45", "iPhone · Safari", "203.0.113.*"},
		{"Windows Edge IPv4", windowsEdge, "198.51.100.9", "Windows · Edge", "198.51.100.*"},
		{"Android Chrome IPv6", androidChrome, "2001:db8:abcd:1234:5678::1", "Android · Chrome", "2001:db8:abcd:1234::*"},
		{"unknown values", "secret custom agent", "not-an-ip", "其他设备 · 其他浏览器", "—"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := SummarizeClient(test.ua, net.ParseIP(test.ip))
			if result.DeviceLabel != test.label || result.ClientNetwork != test.network {
				t.Fatalf("SummarizeClient() = %#v, want label %q and network %q", result, test.label, test.network)
			}
			if strings.Contains(result.DeviceLabel, "secret") {
				t.Fatalf("raw user agent crossed projection: %#v", result)
			}
			if len(result.DeviceLabel) > 80 {
				t.Fatalf("DeviceLabel is %d UTF-8 bytes, want at most 80", len(result.DeviceLabel))
			}
		})
	}
}
