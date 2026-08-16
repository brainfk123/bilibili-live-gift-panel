package platform

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Config contains the hosted process configuration. Callers must never log the
// complete value because it contains the MySQL DSN and secret file locations.
type Config struct {
	ListenAddr        string
	MySQLDSN          string
	EncryptionKeyFile string
	HMACKeyFile       string
}

// Load reads and validates the hosted process environment.
func Load() (Config, error) {
	listenAddr, err := required("HOSTED_LISTEN_ADDR")
	if err != nil {
		return Config{}, err
	}
	dsn, err := required("HOSTED_MYSQL_DSN")
	if err != nil {
		return Config{}, err
	}
	encryptionKeyFile, err := required("HOSTED_ENCRYPTION_KEY_FILE")
	if err != nil {
		return Config{}, err
	}
	hmacKeyFile, err := required("HOSTED_HMAC_KEY_FILE")
	if err != nil {
		return Config{}, err
	}

	if err := validateListenAddr(listenAddr); err != nil {
		return Config{}, fmt.Errorf("HOSTED_LISTEN_ADDR must be a literal loopback address: %w", err)
	}

	return Config{
		ListenAddr:        listenAddr,
		MySQLDSN:          dsn,
		EncryptionKeyFile: encryptionKeyFile,
		HMACKeyFile:       hmacKeyFile,
	}, nil
}

func required(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("required environment variable %s is missing", name)
	}
	return value, nil
}

func validateListenAddr(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("invalid host and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("host is not loopback")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port is invalid")
	}
	return nil
}
