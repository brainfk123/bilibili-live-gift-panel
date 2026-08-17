package platform

import (
	"strings"
	"testing"
)

func TestLoadRequiresEveryHostedVariable(t *testing.T) {
	required := []string{
		"HOSTED_LISTEN_ADDR",
		"HOSTED_MYSQL_DSN",
		"HOSTED_ENCRYPTION_KEY_FILE",
		"HOSTED_HMAC_KEY_FILE",
	}

	for _, missing := range required {
		t.Run(missing, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(missing, "")

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("Load() error = %v, want missing variable %s", err, missing)
			}
		})
	}
}

func TestLoadAcceptsOnlyLiteralLoopbackListeners(t *testing.T) {
	accepted := []string{"127.0.0.1:12500", "127.42.0.9:8080", "[::1]:12500"}
	for _, address := range accepted {
		t.Run("accept_"+address, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv("HOSTED_LISTEN_ADDR", address)

			config, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if config.ListenAddr != address {
				t.Fatalf("ListenAddr = %q, want %q", config.ListenAddr, address)
			}
		})
	}

	rejected := []string{
		"0.0.0.0:12500",
		"[::]:12500",
		"::",
		":12500",
		"12500",
		"localhost:12500",
		"192.168.1.20:12500",
		"8.8.8.8:12500",
		"127.0.0.1:0",
		"127.0.0.1:",
	}
	for _, address := range rejected {
		t.Run("reject_"+address, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv("HOSTED_LISTEN_ADDR", address)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted non-loopback or malformed address %q", address)
			}
		})
	}
}

func TestLoadAllowsOnlyTheExactContainerListenerWhenExplicitlyEnabled(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("HOSTED_LISTEN_ADDR", "0.0.0.0:12500")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted the container listener while container mode was disabled")
	}

	t.Setenv("HOSTED_CONTAINER_LISTEN", "true")
	config, err := Load()
	if err != nil {
		t.Fatalf("Load() rejected the exact container listener: %v", err)
	}
	if config.ListenAddr != "0.0.0.0:12500" {
		t.Fatalf("ListenAddr = %q, want exact container listener", config.ListenAddr)
	}

	for _, address := range []string{"0.0.0.0:12501", "0.0.0.0:80", "[::]:12500", "192.0.2.10:12500"} {
		t.Run(address, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv("HOSTED_CONTAINER_LISTEN", "true")
			t.Setenv("HOSTED_LISTEN_ADDR", address)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted non-exact container listener %q", address)
			}
		})
	}
}

func TestLoadRejectsMalformedContainerListenMode(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("HOSTED_CONTAINER_LISTEN", "sometimes")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "HOSTED_CONTAINER_LISTEN") {
		t.Fatalf("Load() error = %v, want stable container mode validation", err)
	}
}

func TestLoadErrorsDoNotExposeSecrets(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("HOSTED_LISTEN_ADDR", "0.0.0.0:12500")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want invalid listener error")
	}
	for _, secret := range []string{"hosted-secret-dsn", "encryption-secret-path", "hmac-secret-path"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("Load() error exposed secret value %q: %v", secret, err)
		}
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("HOSTED_LISTEN_ADDR", "127.0.0.1:12500")
	t.Setenv("HOSTED_MYSQL_DSN", "hosted-secret-dsn")
	t.Setenv("HOSTED_ENCRYPTION_KEY_FILE", "encryption-secret-path")
	t.Setenv("HOSTED_HMAC_KEY_FILE", "hmac-secret-path")
}
