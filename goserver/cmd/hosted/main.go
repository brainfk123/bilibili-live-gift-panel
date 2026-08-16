package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

	server := &http.Server{
		Addr:              config.ListenAddr,
		Handler:           app.New(app.Dependencies{DB: store}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.ListenAndServe()
	}()
	slog.Info("hosted service listening", "address", config.ListenAddr)

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve hosted HTTP: %w", err)
	case <-processContext.Done():
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
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
