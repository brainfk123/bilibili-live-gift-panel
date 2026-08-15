package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/cosstore"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/httpapi"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/service"
)

const defaultListenAddress = "127.0.0.1:12450"
const defaultChannelKey = "channels/stable/latest.json"

type config struct {
	listenAddress string
	bucket        string
	region        string
	secretID      string
	secretKey     string
	channelKey    string
}

type standardLogger struct{ logger *log.Logger }

func (logger standardLogger) Error(requestID, code string, cause error) {
	logger.logger.Printf("request_id=%s code=%s cause=%v", requestID, code, cause)
}

func main() {
	logger := log.New(os.Stderr, "update-api ", log.LstdFlags|log.LUTC)
	configuration, err := loadConfig()
	if err != nil {
		logger.Printf("startup cause=%v", err)
		os.Exit(1)
	}

	store, err := cosstore.New(configuration.bucket, configuration.region, configuration.secretID, configuration.secretKey, nil)
	if err != nil {
		logger.Printf("startup cause=%v", err)
		os.Exit(1)
	}
	handler := httpapi.New(service.New(store, configuration.channelKey, time.Now), nil, standardLogger{logger})
	server := &http.Server{
		Addr:              configuration.listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	logger.Printf("startup listen=%s", configuration.listenAddress)
	if status := serve(server, signals, logger); status != 0 {
		os.Exit(status)
	}
}

func serve(server *http.Server, signals <-chan os.Signal, logger *log.Logger) int {
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()

	select {
	case signal := <-signals:
		logger.Printf("shutdown signal=%s", signal)
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Printf("shutdown cause=%v", err)
			return 1
		}
		logger.Printf("shutdown complete")
		return 0
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("shutdown cause=%v", err)
			return 1
		}
		return 0
	}
}

func loadConfig() (config, error) {
	configuration := config{
		listenAddress: valueOrDefault("UPDATE_API_LISTEN", defaultListenAddress),
		bucket:        os.Getenv("COS_BUCKET"),
		region:        os.Getenv("COS_REGION"),
		secretID:      os.Getenv("COS_SECRET_ID"),
		secretKey:     os.Getenv("COS_SECRET_KEY"),
		channelKey:    defaultChannelKey,
	}
	if err := validateLoopbackAddress(configuration.listenAddress); err != nil {
		return config{}, err
	}
	for _, variable := range []struct {
		name  string
		value string
	}{
		{"COS_BUCKET", configuration.bucket},
		{"COS_REGION", configuration.region},
		{"COS_SECRET_ID", configuration.secretID},
		{"COS_SECRET_KEY", configuration.secretKey},
	} {
		if variable.value == "" {
			return config{}, fmt.Errorf("%s is required", variable.name)
		}
	}
	return configuration, nil
}

func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return errors.New("UPDATE_API_LISTEN must be a host:port address")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("UPDATE_API_LISTEN must use a loopback IP address")
	}
	return nil
}
