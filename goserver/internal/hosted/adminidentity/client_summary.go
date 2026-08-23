package adminidentity

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
)

// ClientSummary is the allowlisted client metadata that may be persisted or
// returned by administrator session and audit projections.
type ClientSummary struct {
	DeviceLabel   string
	ClientNetwork string
}

// SummarizeClient projects untrusted client metadata into a version-free,
// allowlisted device and browser label plus a masked network prefix.
func SummarizeClient(userAgent string, address net.IP) ClientSummary {
	return ClientSummary{
		DeviceLabel:   clientDevice(userAgent) + " · " + clientBrowser(userAgent),
		ClientNetwork: maskedClientNetwork(address),
	}
}

func clientDevice(userAgent string) string {
	switch {
	case strings.Contains(userAgent, "iPhone"):
		return "iPhone"
	case strings.Contains(userAgent, "iPad"):
		return "iPad"
	case strings.Contains(userAgent, "Android"):
		return "Android"
	case strings.Contains(userAgent, "Windows"):
		return "Windows"
	case strings.Contains(userAgent, "Macintosh"), strings.Contains(userAgent, "Mac OS X"):
		return "macOS"
	case strings.Contains(userAgent, "Linux"):
		return "Linux"
	default:
		return "其他设备"
	}
}

func clientBrowser(userAgent string) string {
	switch {
	case strings.Contains(userAgent, "Edg/"), strings.Contains(userAgent, "EdgA/"), strings.Contains(userAgent, "EdgiOS/"), strings.Contains(userAgent, "Edge/"):
		return "Edge"
	case strings.Contains(userAgent, "Firefox/"), strings.Contains(userAgent, "FxiOS/"):
		return "Firefox"
	case strings.Contains(userAgent, "Chrome/"), strings.Contains(userAgent, "CriOS/"):
		return "Chrome"
	case strings.Contains(userAgent, "Safari/"):
		return "Safari"
	default:
		return "其他浏览器"
	}
}

func maskedClientNetwork(address net.IP) string {
	if ipv4 := address.To4(); ipv4 != nil {
		return fmt.Sprintf("%d.%d.%d.*", ipv4[0], ipv4[1], ipv4[2])
	}
	if ipv6 := address.To16(); ipv6 != nil {
		return fmt.Sprintf("%x:%x:%x:%x::*",
			binary.BigEndian.Uint16(ipv6[0:2]),
			binary.BigEndian.Uint16(ipv6[2:4]),
			binary.BigEndian.Uint16(ipv6[4:6]),
			binary.BigEndian.Uint16(ipv6[6:8]),
		)
	}
	return "—"
}
