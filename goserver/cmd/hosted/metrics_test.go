package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/app"
	"bilibili-live-gift-panel/internal/hosted/biligateway"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
	hostedruntime "bilibili-live-gift-panel/internal/hosted/runtime"

	"github.com/DATA-DOG/go-sqlmock"
)

type stubRuntimeMetrics struct {
	value hostedruntime.Metrics
}

func (metrics stubRuntimeMetrics) Metrics() hostedruntime.Metrics { return metrics.value }

type stubRoomMetrics struct {
	value roomsource.Metrics
}

func (metrics stubRoomMetrics) Metrics() roomsource.Metrics { return metrics.value }

type stubGatewayMetrics struct {
	value biligateway.Metrics
}

func (metrics stubGatewayMetrics) Metrics() biligateway.Metrics { return metrics.value }

func TestHostedMetricsAssemblesIdentityFreeSnapshotFromRealSources(t *testing.T) {
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "daily.next"), []byte("2026-08-19\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	certPath := writeTestCertificate(t, filepath.Join(root, "tls.crt"), now, now.Add(40*24*time.Hour))
	provider := hostedMetrics{
		now:     func() time.Time { return now },
		process: func() (float64, uint64, error) { return 12.5, 4096, nil },
		ping: func(ctx context.Context) (time.Duration, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("mysql ping missing deadline")
			}
			return 15 * time.Millisecond, nil
		},
		runtime: stubRuntimeMetrics{value: hostedruntime.Metrics{ActiveAccounts: 3, QueueDepth: 4, QueueDepthMax: 4, DegradedAccounts: 1, RejectingAccounts: 1}},
		rooms:   stubRoomMetrics{value: roomsource.Metrics{DistinctRooms: 2, Healthy: true, Reconnects: 5}},
		gateway: stubGatewayMetrics{value: biligateway.Metrics{RiskEvents: 6, RateLimited: 2, Failures: 3, BreakerOpen: true}},
		migrations: func(context.Context) (uint64, uint64, uint64, error) {
			return 8, 9, 7, nil
		},
		backupRoot:  root,
		certificate: certPath,
	}
	snapshot, err := provider.Metrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ProcessCPUSeconds != 12.5 || snapshot.ProcessResidentMemoryBytes != 4096 {
		t.Fatalf("process snapshot = %#v", snapshot)
	}
	if !snapshot.MySQLHealthy || snapshot.MySQLLatencySeconds != 0.015 {
		t.Fatalf("mysql snapshot = %#v", snapshot)
	}
	if snapshot.ActiveAccounts != 3 || snapshot.DistinctRoomSources != 2 || snapshot.RuntimeQueueDepth != 4 {
		t.Fatalf("runtime snapshot = %#v", snapshot)
	}
	if snapshot.BiliReconnects != 5 || snapshot.BiliRiskEvents != 6 || snapshot.BiliRateLimited != 2 || snapshot.BiliFailures != 3 || !snapshot.BiliBreakerOpen {
		t.Fatalf("bili snapshot = %#v", snapshot)
	}
	if snapshot.MigrationsPending != 8 || snapshot.MigrationsFailed != 9 || snapshot.MigrationsApplied != 7 {
		t.Fatalf("migration snapshot = %#v", snapshot)
	}
	if snapshot.BackupAgeSeconds != 0 || !snapshot.CertificateValid || snapshot.CertificateExpirySeconds == 0 {
		t.Fatalf("host snapshot = %#v", snapshot)
	}
	rendered := strings.ToLower(snapshotString(snapshot))
	for _, forbidden := range []string{"cookie", "token", "nickname", "secret"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("snapshot exposed %q: %s", forbidden, rendered)
		}
	}
}

func TestHostedMetricsBackupAgeUsesDailyCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "daily.next"), []byte("2026-08-16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := hostedMetrics{
		now:         func() time.Time { return now },
		process:     func() (float64, uint64, error) { return 0, 1, nil },
		ping:        func(context.Context) (time.Duration, error) { return time.Millisecond, nil },
		runtime:     stubRuntimeMetrics{},
		rooms:       stubRoomMetrics{value: roomsource.Metrics{Healthy: true}},
		gateway:     stubGatewayMetrics{},
		migrations:  func(context.Context) (uint64, uint64, uint64, error) { return 0, 0, 0, nil },
		backupRoot:  root,
		certificate: writeTestCertificate(t, filepath.Join(root, "tls.crt"), now, now.Add(40*24*time.Hour)),
	}
	snapshot, err := provider.Metrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.BackupAgeSeconds != 2*24*60*60 {
		t.Fatalf("BackupAgeSeconds = %d", snapshot.BackupAgeSeconds)
	}
}

func TestHostedMetricsFailsClosedWithoutHostMonitoringInputs(t *testing.T) {
	provider := hostedMetrics{
		now:        func() time.Time { return time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC) },
		process:    func() (float64, uint64, error) { return 0, 1, nil },
		ping:       func(context.Context) (time.Duration, error) { return time.Millisecond, nil },
		migrations: func(context.Context) (uint64, uint64, uint64, error) { return 0, 0, 0, nil },
	}
	if _, err := provider.Metrics(context.Background()); err == nil {
		t.Fatal("Metrics() succeeded without backup and certificate inputs")
	}
}

func TestSampleProcessUsesLinuxProcessCPUAndResidentSet(t *testing.T) {
	readFile := func(path string) ([]byte, error) {
		switch path {
		case "/proc/self/schedstat":
			return []byte("2500000000 17 3\n"), nil
		case "/proc/self/statm":
			return []byte("100 7 2 1 0 0 0\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}
	cpu, rss, err := sampleProcessFrom(readFile, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if cpu != 2.5 || rss != 7*4096 {
		t.Fatalf("process sample = cpu %v rss %d", cpu, rss)
	}
}

func TestCountMigrationsReadsStatusTotalsWithoutRowPayloads(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM migration_jobs WHERE status IN \('previewed', 'pending'\)`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM migration_jobs WHERE status IN \('previewed', 'pending'\) AND expires_at < CURRENT_TIMESTAMP\(6\)`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(8))
	pending, failed, applied, err := countMigrations(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 4 || failed != 1 || applied != 8 {
		t.Fatalf("counts = %d %d %d", pending, failed, applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestComposeWiresPrivateMetricsProvider(t *testing.T) {
	handler := composeHostedHTTPWithRuntimeOBSStaticAndMetrics(
		healthyHostedDatabase{}, statusHandler(http.StatusAccepted), statusHandler(http.StatusAccepted),
		statusHandler(http.StatusAccepted), nil, nil, nil, nil, nil, nil,
		stubAppMetrics{snapshot: app.MetricsSnapshot{ActiveAccounts: 3, DistinctRoomSources: 2}},
		"runtime-csrf",
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "hosted_active_accounts 3\n") || !strings.Contains(body, "hosted_distinct_room_sources 2\n") {
		t.Fatalf("metrics body = %q", body)
	}
}

type stubAppMetrics struct {
	snapshot app.MetricsSnapshot
}

func (metrics stubAppMetrics) Metrics(context.Context) (app.MetricsSnapshot, error) {
	return metrics.snapshot, nil
}

func writeTestCertificate(t *testing.T, path string, now, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hosted-metrics-test"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func snapshotString(snapshot app.MetricsSnapshot) string {
	return strings.Join([]string{
		"cpu", "rss", "mysql", "accounts", "rooms", "queue", "bili", "migrations", "backup", "certificate",
	}, " ")
}
