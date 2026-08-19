package main

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/hosted/app"
	"bilibili-live-gift-panel/internal/hosted/biligateway"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
	hostedruntime "bilibili-live-gift-panel/internal/hosted/runtime"
)

type runtimeMetricsSource interface {
	Metrics() hostedruntime.Metrics
}

type roomMetricsSource interface {
	Metrics() roomsource.Metrics
}

type gatewayMetricsSource interface {
	Metrics() biligateway.Metrics
}

type hostedMetrics struct {
	now         func() time.Time
	process     func() (float64, uint64, error)
	ping        func(context.Context) (time.Duration, error)
	runtime     runtimeMetricsSource
	rooms       roomMetricsSource
	gateway     gatewayMetricsSource
	migrations  func(context.Context) (uint64, uint64, uint64, error)
	backupRoot  string
	certificate string
}

func (metrics hostedMetrics) Metrics(ctx context.Context) (app.MetricsSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	if metrics.now != nil {
		now = metrics.now()
	}
	cpu, rss, err := metrics.process()
	if err != nil {
		return app.MetricsSnapshot{}, err
	}
	pingContext, cancelPing := context.WithTimeout(ctx, 2*time.Second)
	defer cancelPing()
	latency, pingErr := metrics.ping(pingContext)
	pending, failed, applied := uint64(0), uint64(0), uint64(0)
	if metrics.migrations != nil {
		pending, failed, applied, err = metrics.migrations(ctx)
		if err != nil {
			return app.MetricsSnapshot{}, err
		}
	}
	backupAge, err := backupAgeSeconds(metrics.backupRoot, now)
	if err != nil {
		return app.MetricsSnapshot{}, err
	}
	expiry, valid, err := certificateExpiry(metrics.certificate, now)
	if err != nil {
		return app.MetricsSnapshot{}, err
	}
	snapshot := app.MetricsSnapshot{
		ProcessCPUSeconds:          cpu,
		ProcessResidentMemoryBytes: rss,
		MySQLHealthy:               pingErr == nil,
		MySQLLatencySeconds:        latency.Seconds(),
		MigrationsPending:          pending,
		MigrationsFailed:           failed,
		MigrationsApplied:          applied,
		BackupAgeSeconds:           backupAge,
		CertificateExpirySeconds:   expiry,
		CertificateValid:           valid,
	}
	if metrics.runtime != nil {
		runtimeMetrics := metrics.runtime.Metrics()
		snapshot.ActiveAccounts = runtimeMetrics.ActiveAccounts
		snapshot.RuntimeQueueDepth = runtimeMetrics.QueueDepth
		snapshot.RuntimeQueueDepthMax = runtimeMetrics.QueueDepthMax
		snapshot.RuntimeDegradedAccounts = runtimeMetrics.DegradedAccounts
		snapshot.RuntimeRejectingAccounts = runtimeMetrics.RejectingAccounts
	}
	if metrics.rooms != nil {
		roomMetrics := metrics.rooms.Metrics()
		snapshot.DistinctRoomSources = roomMetrics.DistinctRooms
		snapshot.RoomSourcesHealthy = roomMetrics.Healthy
		snapshot.BiliReconnects = roomMetrics.Reconnects
	}
	if metrics.gateway != nil {
		gatewayMetrics := metrics.gateway.Metrics()
		snapshot.BiliRiskEvents = gatewayMetrics.RiskEvents
		snapshot.BiliRateLimited = gatewayMetrics.RateLimited
		snapshot.BiliFailures = gatewayMetrics.Failures
		snapshot.BiliBreakerOpen = gatewayMetrics.BreakerOpen
	}
	return snapshot, nil
}

func backupAgeSeconds(root string, now time.Time) (uint64, error) {
	if root == "" {
		return 0, errors.New("backup metrics are not configured")
	}
	contents, err := os.ReadFile(filepath.Join(root, "daily.next"))
	if err != nil {
		return 0, err
	}
	next, err := time.Parse("2006-01-02", strings.TrimSpace(string(contents)))
	if err != nil {
		return 0, err
	}
	today := now.UTC().Truncate(24 * time.Hour)
	if !next.Before(today) {
		return 0, nil
	}
	return uint64(today.Sub(next).Seconds()), nil
}

func certificateExpiry(path string, now time.Time) (uint64, bool, error) {
	if path == "" {
		return 0, false, errors.New("certificate metrics are not configured")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	block, _ := pem.Decode(contents)
	if block == nil || block.Type != "CERTIFICATE" {
		return 0, false, errors.New("certificate is invalid")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return 0, false, err
	}
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return 0, false, nil
	}
	return uint64(cert.NotAfter.Sub(now).Seconds()), true, nil
}

func countMigrations(ctx context.Context, database *sql.DB) (uint64, uint64, uint64, error) {
	if database == nil {
		return 0, 0, 0, errors.New("mysql store is not initialized")
	}
	var pending, failed, applied uint64
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM migration_jobs WHERE status IN ('previewed', 'pending')").Scan(&pending); err != nil {
		return 0, 0, 0, err
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM migration_jobs WHERE status IN ('previewed', 'pending') AND expires_at < CURRENT_TIMESTAMP(6)").Scan(&failed); err != nil {
		return 0, 0, 0, err
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
		return 0, 0, 0, err
	}
	return pending, failed, applied, nil
}

func sampleProcess() (float64, uint64, error) {
	cpu, rss, err := sampleProcessFrom(os.ReadFile, os.Getpagesize())
	if err == nil {
		return cpu, rss, nil
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return 0, memory.Sys, nil
}

func sampleProcessFrom(readFile func(string) ([]byte, error), pageSize int) (float64, uint64, error) {
	if readFile == nil || pageSize <= 0 {
		return 0, 0, errors.New("process metrics are unavailable")
	}
	scheduling, err := readFile("/proc/self/schedstat")
	if err != nil {
		return 0, 0, err
	}
	schedulingFields := strings.Fields(string(scheduling))
	if len(schedulingFields) < 1 {
		return 0, 0, errors.New("process CPU metrics are invalid")
	}
	cpuNanoseconds, err := strconv.ParseUint(schedulingFields[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	memory, err := readFile("/proc/self/statm")
	if err != nil {
		return 0, 0, err
	}
	memoryFields := strings.Fields(string(memory))
	if len(memoryFields) < 2 {
		return 0, 0, errors.New("process memory metrics are invalid")
	}
	residentPages, err := strconv.ParseUint(memoryFields[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	pageBytes := uint64(pageSize)
	if residentPages > ^uint64(0)/pageBytes {
		return 0, 0, errors.New("process memory metrics overflow")
	}
	return float64(cpuNanoseconds) / float64(time.Second), residentPages * pageBytes, nil
}
