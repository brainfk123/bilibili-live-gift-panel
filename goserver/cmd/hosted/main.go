package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bilibili-live-gift-panel/internal/hosted/app"
	"bilibili-live-gift-panel/internal/hosted/platform"
	"bilibili-live-gift-panel/internal/hosted/store/mysqlstore"
)

const shutdownTimeout = 30 * time.Second

type serverLifecycle interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}

type listenFunc func(network, address string) (net.Listener, error)

func main() {
	if err := run(); err != nil {
		slog.Error("hosted service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	processContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	config, err := platform.Load()
	if err != nil {
		return fmt.Errorf("load hosted configuration: %w", err)
	}

	store, err := mysqlstore.Open(processContext, config.MySQLDSN)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(processContext); err != nil {
		return fmt.Errorf("migrate hosted database: %w", err)
	}

	server := newHTTPServer(config.ListenAddr, app.New(app.Dependencies{DB: store}))
	return serveHTTP(
		processContext,
		server,
		config.ListenAddr,
		net.Listen,
		shutdownTimeout,
		func() { slog.Info("hosted service listening", "address", config.ListenAddr) },
	)
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func serveHTTP(
	ctx context.Context,
	server serverLifecycle,
	address string,
	listen listenFunc,
	gracePeriod time.Duration,
	onListening func(),
) error {
	listener, err := listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen hosted HTTP: %w", err)
	}
	defer listener.Close()
	if onListening != nil {
		onListening()
	}

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve hosted HTTP: %w", err)
	case <-ctx.Done():
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), gracePeriod)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown hosted HTTP: %w", err)
	}

	if err := <-serveErrors; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve hosted HTTP during shutdown: %w", err)
	}
	return nil
}
