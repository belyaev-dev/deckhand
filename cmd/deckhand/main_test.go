package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/api"
	"github.com/deckhand-for-cnpg/deckhand/internal/k8s"
	"github.com/deckhand-for-cnpg/deckhand/internal/metrics"
	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

func TestMainBuildsServer(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := k8s.RuntimeConfig{
		ListenAddr: "127.0.0.1:0",
		Namespaces: []string{"team-a", "team-b"},
	}

	deps := defaultAppDeps()
	deps.bootstrapRuntime = fakeBootstrapRuntime(t)

	bootstrap, err := deps.bootstrapRuntime(cfg)
	if err != nil {
		t.Fatalf("bootstrapRuntime() error: %v", err)
	}
	if bootstrap == nil {
		t.Fatal("bootstrap is nil")
	}
	if bootstrap.Scheme == nil {
		t.Fatal("bootstrap scheme is nil")
	}
	if len(bootstrap.CacheOptions.DefaultNamespaces) != 2 {
		t.Fatalf("DefaultNamespaces length = %d, want 2", len(bootstrap.CacheOptions.DefaultNamespaces))
	}

	runtimeStore := deps.newStore()
	if runtimeStore == nil {
		t.Fatal("runtime store is nil")
	}

	router := deps.newRouter(api.ServerDeps{Logger: logger, Store: runtimeStore})
	if router == nil {
		t.Fatal("router is nil")
	}

	server := deps.newServer(cfg.ListenAddr, router, logger)
	if server == nil {
		t.Fatal("server is nil")
	}
	if got, want := server.Addr(), cfg.ListenAddr; got != want {
		t.Fatalf("server.Addr() = %q, want %q before startup", got, want)
	}
}

func TestMainGracefulShutdown(t *testing.T) {
	var logBuffer safeBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var serverRef atomic.Pointer[api.Server]
	deps := defaultAppDeps()
	deps.bootstrapRuntime = fakeBootstrapRuntime(t)
	deps.newWatcher = func(_ *k8s.ClientBootstrap, st *store.Store, _ *slog.Logger) (runtimeWatcher, error) {
		return newFakeRuntimeWatcher(st, nil, nil), nil
	}
	deps.newScraper = func(_ *k8s.ClientBootstrap, _ *store.Store, _ *slog.Logger) (runtimeMetricsScraper, error) {
		return newFakeRuntimeScraper(nil, nil), nil
	}
	deps.newServer = func(addr string, handler http.Handler, logger *slog.Logger) *api.Server {
		s := api.NewServer(addr, handler, logger)
		serverRef.Store(s)
		return s
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, k8s.RuntimeConfig{ListenAddr: "127.0.0.1:0"}, logger, deps)
	}()

	addr := waitForServerAddress(t, func() *api.Server { return serverRef.Load() })

	assertJSONEndpoint(t, "http://"+addr+"/healthz", http.StatusOK, func(body []byte) {
		var health struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body, &health); err != nil {
			t.Fatalf("unmarshal /healthz response: %v", err)
		}
		if health.Status != "ok" {
			t.Fatalf("health status = %q, want %q", health.Status, "ok")
		}
	})

	assertJSONEndpoint(t, "http://"+addr+"/api/", http.StatusOK, func(body []byte) {
		var payload struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal /api/ response: %v", err)
		}
		if payload.Version != "0.1.0" {
			t.Fatalf("api version = %q, want %q", payload.Version, "0.1.0")
		}
	})

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run() to stop")
	}

	if _, err := http.Get("http://" + addr + "/healthz"); err == nil {
		t.Fatal("expected HTTP requests to fail after shutdown")
	}

	logs := logBuffer.String()
	for _, fragment := range []string{
		"deckhand starting",
		"kubernetes runtime ready",
		"waiting for cnpg watcher readiness",
		"cnpg watcher ready; starting metrics scraper",
		"waiting for metrics scraper readiness",
		"metrics scraper ready; starting websocket hub",
		"waiting for websocket hub readiness",
		"websocket hub ready; starting HTTP server",
		"shutdown requested",
		"deckhand stopped",
	} {
		if !bytes.Contains([]byte(logs), []byte(fragment)) {
			t.Fatalf("expected logs to contain %q, got %s", fragment, logs)
		}
	}
}

func TestMainWiresScraper(t *testing.T) {
	var logBuffer safeBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	releaseWatcherReady := make(chan struct{})
	releaseScraperReady := make(chan struct{})

	var watcherRef atomic.Pointer[fakeRuntimeWatcher]
	var scraperRef atomic.Pointer[fakeRuntimeScraper]
	var serverRef atomic.Pointer[api.Server]

	deps := defaultAppDeps()
	deps.bootstrapRuntime = fakeBootstrapRuntime(t)
	deps.newWatcher = func(_ *k8s.ClientBootstrap, st *store.Store, _ *slog.Logger) (runtimeWatcher, error) {
		w := newFakeRuntimeWatcher(st, releaseWatcherReady, func(st *store.Store) error {
			if err := st.UpsertCluster(mainTestCluster("team-a", "alpha")); err != nil {
				return err
			}
			if err := st.UpsertBackup(mainTestBackup("team-a", "alpha-backup", "alpha")); err != nil {
				return err
			}
			if err := st.UpsertScheduledBackup(mainTestScheduledBackup("team-a", "alpha-nightly", "alpha")); err != nil {
				return err
			}
			return nil
		})
		watcherRef.Store(w)
		return w, nil
	}
	deps.newScraper = func(_ *k8s.ClientBootstrap, _ *store.Store, _ *slog.Logger) (runtimeMetricsScraper, error) {
		s := newFakeRuntimeScraper(releaseScraperReady, map[string]metrics.ClusterMetrics{
			metricsKey("team-a", "alpha"): mainTestMetrics("team-a", "alpha"),
		})
		scraperRef.Store(s)
		return s, nil
	}
	deps.newServer = func(addr string, handler http.Handler, logger *slog.Logger) *api.Server {
		s := api.NewServer(addr, handler, logger)
		serverRef.Store(s)
		return s
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, k8s.RuntimeConfig{ListenAddr: "127.0.0.1:0"}, logger, deps)
	}()

	waitForWatcherStart(t, func() *fakeRuntimeWatcher { return watcherRef.Load() })
	waitForServerConstruction(t, func() *api.Server { return serverRef.Load() })

	srv := serverRef.Load()
	if got, want := srv.Addr(), "127.0.0.1:0"; got != want {
		t.Fatalf("server.Addr() before watcher readiness = %q, want %q", got, want)
	}
	if strings.Contains(logBuffer.String(), "starting HTTP server") {
		t.Fatalf("expected HTTP server to stay unstarted before watcher readiness, logs=%s", logBuffer.String())
	}

	close(releaseWatcherReady)
	waitForScraperStart(t, func() *fakeRuntimeScraper { return scraperRef.Load() })

	if got, want := srv.Addr(), "127.0.0.1:0"; got != want {
		t.Fatalf("server.Addr() before scraper readiness = %q, want %q", got, want)
	}
	if strings.Contains(logBuffer.String(), "starting HTTP server") {
		t.Fatalf("expected HTTP server to stay unstarted before scraper readiness, logs=%s", logBuffer.String())
	}

	close(releaseScraperReady)

	addr := waitForServerAddress(t, func() *api.Server { return serverRef.Load() })

	assertJSONEndpoint(t, "http://"+addr+"/api/clusters/team-a/alpha/metrics", http.StatusOK, func(body []byte) {
		var response api.ClusterMetricsResponse
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatalf("unmarshal /api/clusters/team-a/alpha/metrics response: %v", err)
		}
		if got, want := response.OverallHealth, "warning"; got != want {
			t.Fatalf("response.OverallHealth = %q, want %q", got, want)
		}
		if got := len(response.Instances); got != 2 {
			t.Fatalf("len(response.Instances) = %d, want 2", got)
		}
		if got, want := response.Instances[0].Connections.Total, 10; got != want {
			t.Fatalf("response.Instances[0].Connections.Total = %d, want %d", got, want)
		}
		if got, want := response.Instances[1].Health, "unknown"; got != want {
			t.Fatalf("response.Instances[1].Health = %q, want %q", got, want)
		}
		if strings.Contains(string(body), "10.0.0.12") {
			t.Fatalf("metrics endpoint leaked pod IP: %s", string(body))
		}
	})

	assertJSONEndpoint(t, "http://"+addr+"/api/clusters/team-a/alpha", http.StatusOK, func(body []byte) {
		var response api.ClusterDetailResponse
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatalf("unmarshal /api/clusters/team-a/alpha response: %v", err)
		}
		if response.Cluster.CurrentPrimary != "alpha-1" {
			t.Fatalf("response.Cluster.CurrentPrimary = %q, want %q", response.Cluster.CurrentPrimary, "alpha-1")
		}
		if got := len(response.Backups); got != 1 {
			t.Fatalf("len(response.Backups) = %d, want 1", got)
		}
		if got := len(response.ScheduledBackups); got != 1 {
			t.Fatalf("len(response.ScheduledBackups) = %d, want 1", got)
		}
	})

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run() to stop")
	}

	logs := logBuffer.String()
	for _, fragment := range []string{
		"waiting for cnpg watcher readiness",
		"cnpg watcher ready; starting metrics scraper",
		"waiting for metrics scraper readiness",
		"metrics scraper ready; starting websocket hub",
		"waiting for websocket hub readiness",
		"websocket hub ready; starting HTTP server",
		"starting HTTP server",
		"/api/clusters/team-a/alpha/metrics",
	} {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected logs to contain %q, got %s", fragment, logs)
		}
	}
}

func TestMainWaitsForScraperReady(t *testing.T) {
	var logBuffer safeBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	releaseWatcherReady := make(chan struct{})
	releaseScraperReady := make(chan struct{})

	var watcherRef atomic.Pointer[fakeRuntimeWatcher]
	var scraperRef atomic.Pointer[fakeRuntimeScraper]
	var serverRef atomic.Pointer[api.Server]

	deps := defaultAppDeps()
	deps.bootstrapRuntime = fakeBootstrapRuntime(t)
	deps.newWatcher = func(_ *k8s.ClientBootstrap, st *store.Store, _ *slog.Logger) (runtimeWatcher, error) {
		w := newFakeRuntimeWatcher(st, releaseWatcherReady, nil)
		watcherRef.Store(w)
		return w, nil
	}
	deps.newScraper = func(_ *k8s.ClientBootstrap, _ *store.Store, _ *slog.Logger) (runtimeMetricsScraper, error) {
		s := newFakeRuntimeScraper(releaseScraperReady, nil)
		scraperRef.Store(s)
		return s, nil
	}
	deps.newServer = func(addr string, handler http.Handler, logger *slog.Logger) *api.Server {
		s := api.NewServer(addr, handler, logger)
		serverRef.Store(s)
		return s
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, k8s.RuntimeConfig{ListenAddr: "127.0.0.1:0"}, logger, deps)
	}()

	waitForWatcherStart(t, func() *fakeRuntimeWatcher { return watcherRef.Load() })
	waitForServerConstruction(t, func() *api.Server { return serverRef.Load() })
	close(releaseWatcherReady)
	waitForScraperStart(t, func() *fakeRuntimeScraper { return scraperRef.Load() })

	srv := serverRef.Load()
	if got, want := srv.Addr(), "127.0.0.1:0"; got != want {
		t.Fatalf("server.Addr() before scraper readiness = %q, want %q", got, want)
	}
	if strings.Contains(logBuffer.String(), "metrics scraper ready; starting websocket hub") || strings.Contains(logBuffer.String(), "starting HTTP server") {
		t.Fatalf("expected HTTP server to stay blocked on scraper readiness, logs=%s", logBuffer.String())
	}

	close(releaseScraperReady)
	addr := waitForServerAddress(t, func() *api.Server { return serverRef.Load() })

	assertJSONEndpoint(t, "http://"+addr+"/healthz", http.StatusOK, func(body []byte) {
		var health struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body, &health); err != nil {
			t.Fatalf("unmarshal /healthz response: %v", err)
		}
		if health.Status != "ok" {
			t.Fatalf("health status = %q, want %q", health.Status, "ok")
		}
	})

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run() to stop")
	}

	logs := logBuffer.String()
	for _, fragment := range []string{
		"cnpg watcher ready; starting metrics scraper",
		"waiting for metrics scraper readiness",
		"metrics scraper ready; starting websocket hub",
		"waiting for websocket hub readiness",
		"websocket hub ready; starting HTTP server",
	} {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected logs to contain %q, got %s", fragment, logs)
		}
	}
}

func TestMainWiresWebSocketHub(t *testing.T) {
	var logBuffer safeBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	releaseWatcherReady := make(chan struct{})
	releaseScraperReady := make(chan struct{})
	runtimeStore := store.New()

	var watcherRef atomic.Pointer[fakeRuntimeWatcher]
	var scraperRef atomic.Pointer[fakeRuntimeScraper]
	var serverRef atomic.Pointer[api.Server]

	deps := defaultAppDeps()
	deps.bootstrapRuntime = fakeBootstrapRuntime(t)
	deps.newStore = func() *store.Store {
		return runtimeStore
	}
	deps.newWatcher = func(_ *k8s.ClientBootstrap, st *store.Store, _ *slog.Logger) (runtimeWatcher, error) {
		w := newFakeRuntimeWatcher(st, releaseWatcherReady, nil)
		watcherRef.Store(w)
		return w, nil
	}
	deps.newScraper = func(_ *k8s.ClientBootstrap, _ *store.Store, _ *slog.Logger) (runtimeMetricsScraper, error) {
		s := newFakeRuntimeScraper(releaseScraperReady, nil)
		scraperRef.Store(s)
		return s, nil
	}
	deps.newServer = func(addr string, handler http.Handler, logger *slog.Logger) *api.Server {
		s := api.NewServer(addr, handler, logger)
		serverRef.Store(s)
		return s
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, k8s.RuntimeConfig{ListenAddr: "127.0.0.1:0"}, logger, deps)
	}()

	waitForWatcherStart(t, func() *fakeRuntimeWatcher { return watcherRef.Load() })
	waitForServerConstruction(t, func() *api.Server { return serverRef.Load() })
	close(releaseWatcherReady)
	waitForScraperStart(t, func() *fakeRuntimeScraper { return scraperRef.Load() })
	close(releaseScraperReady)

	addr := waitForServerAddress(t, func() *api.Server { return serverRef.Load() })

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatalf("Dial(/ws) error: %v", err)
	}
	defer conn.Close()

	waitForLogContains(t, &logBuffer, "websocket client connected")

	if err := runtimeStore.UpsertCluster(mainTestCluster("team-a", "alpha")); err != nil {
		t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var message api.WSChangeEvent
	if err := conn.ReadJSON(&message); err != nil {
		t.Fatalf("ReadJSON(): %v", err)
	}
	if got, want := message.Type, "store.changed"; got != want {
		t.Fatalf("message.Type = %q, want %q", got, want)
	}
	if got, want := message.Kind, store.ResourceKindCluster; got != want {
		t.Fatalf("message.Kind = %q, want %q", got, want)
	}
	if got, want := message.Action, store.ActionUpsert; got != want {
		t.Fatalf("message.Action = %q, want %q", got, want)
	}
	if got, want := message.Namespace, "team-a"; got != want {
		t.Fatalf("message.Namespace = %q, want %q", got, want)
	}
	if got, want := message.Name, "alpha"; got != want {
		t.Fatalf("message.Name = %q, want %q", got, want)
	}
	if message.OccurredAt.IsZero() {
		t.Fatal("message.OccurredAt is zero")
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close(): %v", err)
	}
	waitForLogContains(t, &logBuffer, "websocket client disconnected")
	waitForLogContains(t, &logBuffer, `"path":"/ws"`)

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run() to stop")
	}

	logs := logBuffer.String()
	for _, fragment := range []string{
		"waiting for websocket hub readiness",
		"websocket hub ready; starting HTTP server",
		"starting websocket hub",
		"websocket client connected",
		"websocket client disconnected",
	} {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected logs to contain %q, got %s", fragment, logs)
		}
	}
}

func TestMainWiresBackupManagement(t *testing.T) {
	var logBuffer safeBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	releaseWatcherReady := make(chan struct{})
	releaseScraperReady := make(chan struct{})

	var watcherRef atomic.Pointer[fakeRuntimeWatcher]
	var scraperRef atomic.Pointer[fakeRuntimeScraper]
	var serverRef atomic.Pointer[api.Server]
	creator := &fakeRuntimeBackupCreator{}

	deps := defaultAppDeps()
	deps.bootstrapRuntime = fakeBootstrapRuntime(t)
	deps.newWatcher = func(_ *k8s.ClientBootstrap, st *store.Store, _ *slog.Logger) (runtimeWatcher, error) {
		w := newFakeRuntimeWatcher(st, releaseWatcherReady, func(st *store.Store) error {
			if err := st.UpsertCluster(mainTestClusterWithBackupConfig("team-a", "alpha")); err != nil {
				return err
			}
			if err := st.UpsertBackup(mainTestBackup("team-a", "alpha-backup", "alpha")); err != nil {
				return err
			}
			if err := st.UpsertScheduledBackup(mainTestScheduledBackup("team-a", "alpha-nightly", "alpha")); err != nil {
				return err
			}
			return nil
		})
		watcherRef.Store(w)
		return w, nil
	}
	deps.newScraper = func(_ *k8s.ClientBootstrap, _ *store.Store, _ *slog.Logger) (runtimeMetricsScraper, error) {
		s := newFakeRuntimeScraper(releaseScraperReady, nil)
		scraperRef.Store(s)
		return s, nil
	}
	deps.newBackupCreator = func(_ *k8s.ClientBootstrap, _ *slog.Logger) (api.BackupCreator, error) {
		creator.create = func(_ context.Context, cluster *cnpgv1.Cluster, options api.BackupCreateOptions) (*cnpgv1.Backup, error) {
			createdAt := metav1.NewTime(time.Date(2026, time.March, 24, 12, 40, 0, 0, time.UTC))
			return &cnpgv1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:         cluster.Namespace,
					Name:              "alpha-backup-manual-001",
					CreationTimestamp: createdAt,
				},
				Spec: cnpgv1.BackupSpec{
					Cluster: cnpgv1.LocalObjectReference{Name: cluster.Name},
					Method:  options.Method,
					Target:  options.Target,
				},
				Status: cnpgv1.BackupStatus{Phase: cnpgv1.BackupPhasePending, Method: options.Method},
			}, nil
		}
		return creator, nil
	}
	deps.newServer = func(addr string, handler http.Handler, logger *slog.Logger) *api.Server {
		s := api.NewServer(addr, handler, logger)
		serverRef.Store(s)
		return s
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, k8s.RuntimeConfig{ListenAddr: "127.0.0.1:0"}, logger, deps)
	}()

	waitForWatcherStart(t, func() *fakeRuntimeWatcher { return watcherRef.Load() })
	waitForServerConstruction(t, func() *api.Server { return serverRef.Load() })
	close(releaseWatcherReady)
	waitForScraperStart(t, func() *fakeRuntimeScraper { return scraperRef.Load() })
	close(releaseScraperReady)

	addr := waitForServerAddress(t, func() *api.Server { return serverRef.Load() })

	assertJSONEndpoint(t, "http://"+addr+"/api/clusters/team-a/alpha/backups", http.StatusOK, func(body []byte) {
		var response api.ClusterBackupsResponse
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatalf("unmarshal /api/clusters/team-a/alpha/backups response: %v", err)
		}
		if got := len(response.Backups); got != 1 {
			t.Fatalf("len(response.Backups) = %d, want 1", got)
		}
		if got := len(response.ScheduledBackups); got != 1 {
			t.Fatalf("len(response.ScheduledBackups) = %d, want 1", got)
		}
	})

	resp, err := http.Post("http://"+addr+"/api/clusters/team-a/alpha/backups", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/clusters/team-a/alpha/backups error: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read POST /api/clusters/team-a/alpha/backups response: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/clusters/team-a/alpha/backups status = %d, want %d; body=%s", resp.StatusCode, http.StatusCreated, string(body))
	}

	var createResponse api.CreateBackupResponse
	if err := json.Unmarshal(body, &createResponse); err != nil {
		t.Fatalf("unmarshal create backup response: %v", err)
	}
	if got, want := createResponse.Backup.Name, "alpha-backup-manual-001"; got != want {
		t.Fatalf("createResponse.Backup.Name = %q, want %q", got, want)
	}
	if creator.calls != 1 {
		t.Fatalf("creator.calls = %d, want 1", creator.calls)
	}
	if got, want := creator.lastOptions.Method, cnpgv1.BackupMethodBarmanObjectStore; got != want {
		t.Fatalf("creator.lastOptions.Method = %q, want %q", got, want)
	}
	if got, want := creator.lastOptions.Target, cnpgv1.BackupTargetStandby; got != want {
		t.Fatalf("creator.lastOptions.Target = %q, want %q", got, want)
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run() to stop")
	}

	logs := logBuffer.String()
	for _, fragment := range []string{
		"/api/clusters/team-a/alpha/backups",
		"backup create accepted",
	} {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected logs to contain %q, got %s", fragment, logs)
		}
	}
}

func TestMainWiresRestoreFlow(t *testing.T) {
	var logBuffer safeBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	releaseWatcherReady := make(chan struct{})
	releaseScraperReady := make(chan struct{})

	var watcherRef atomic.Pointer[fakeRuntimeWatcher]
	var scraperRef atomic.Pointer[fakeRuntimeScraper]
	var serverRef atomic.Pointer[api.Server]
	creator := &fakeRuntimeRestoreCreator{}

	deps := defaultAppDeps()
	deps.bootstrapRuntime = fakeBootstrapRuntime(t)
	deps.newWatcher = func(_ *k8s.ClientBootstrap, st *store.Store, _ *slog.Logger) (runtimeWatcher, error) {
		w := newFakeRuntimeWatcher(st, releaseWatcherReady, func(st *store.Store) error {
			if err := st.UpsertCluster(mainTestClusterWithBackupConfig("team-a", "alpha")); err != nil {
				return err
			}
			if err := st.UpsertBackup(mainRestoreTestBackup("team-a", "alpha-backup-restore-001", "alpha")); err != nil {
				return err
			}
			return nil
		})
		watcherRef.Store(w)
		return w, nil
	}
	deps.newScraper = func(_ *k8s.ClientBootstrap, _ *store.Store, _ *slog.Logger) (runtimeMetricsScraper, error) {
		s := newFakeRuntimeScraper(releaseScraperReady, nil)
		scraperRef.Store(s)
		return s, nil
	}
	deps.newRestoreCreator = func(_ *k8s.ClientBootstrap, _ *slog.Logger) (api.RestoreCreator, error) {
		creator.create = func(_ context.Context, sourceCluster *cnpgv1.Cluster, backup *cnpgv1.Backup, options api.RestoreCreateOptions) (*cnpgv1.Cluster, error) {
			return &cnpgv1.Cluster{
				TypeMeta:   metav1.TypeMeta{APIVersion: cnpgv1.SchemeGroupVersion.String(), Kind: "Cluster"},
				ObjectMeta: metav1.ObjectMeta{Namespace: options.TargetNamespace, Name: options.TargetName},
				Spec: cnpgv1.ClusterSpec{
					Instances: sourceCluster.Spec.Instances,
					Bootstrap: &cnpgv1.BootstrapConfiguration{Recovery: &cnpgv1.BootstrapRecovery{
						Source:         sourceCluster.Name,
						RecoveryTarget: &cnpgv1.RecoveryTarget{BackupID: backup.Status.BackupID},
					}},
				},
			}, nil
		}
		return creator, nil
	}
	deps.newServer = func(addr string, handler http.Handler, logger *slog.Logger) *api.Server {
		s := api.NewServer(addr, handler, logger)
		serverRef.Store(s)
		return s
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, k8s.RuntimeConfig{ListenAddr: "127.0.0.1:0"}, logger, deps)
	}()

	waitForWatcherStart(t, func() *fakeRuntimeWatcher { return watcherRef.Load() })
	waitForServerConstruction(t, func() *api.Server { return serverRef.Load() })
	close(releaseWatcherReady)
	waitForScraperStart(t, func() *fakeRuntimeScraper { return scraperRef.Load() })
	close(releaseScraperReady)

	addr := waitForServerAddress(t, func() *api.Server { return serverRef.Load() })

	assertJSONEndpoint(t, "http://"+addr+"/api/clusters/team-a/alpha/restore", http.StatusOK, func(body []byte) {
		var response api.ClusterRestoreOptionsResponse
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatalf("unmarshal /api/clusters/team-a/alpha/restore response: %v", err)
		}
		if got := len(response.Backups); got != 1 {
			t.Fatalf("len(response.Backups) = %d, want 1", got)
		}
		if got, want := response.Cluster.Name, "alpha"; got != want {
			t.Fatalf("response.Cluster.Name = %q, want %q", got, want)
		}
	})

	resp, err := http.Post("http://"+addr+"/api/clusters/team-a/alpha/restore", "application/json", strings.NewReader(`{"backupName":"alpha-backup-restore-001","targetNamespace":"team-b","targetName":"alpha-restore"}`))
	if err != nil {
		t.Fatalf("POST /api/clusters/team-a/alpha/restore error: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read POST /api/clusters/team-a/alpha/restore response: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/clusters/team-a/alpha/restore status = %d, want %d; body=%s", resp.StatusCode, http.StatusCreated, string(body))
	}

	var createResponse api.CreateRestoreResponse
	if err := json.Unmarshal(body, &createResponse); err != nil {
		t.Fatalf("unmarshal create restore response: %v", err)
	}
	if got, want := createResponse.TargetCluster.Name, "alpha-restore"; got != want {
		t.Fatalf("createResponse.TargetCluster.Name = %q, want %q", got, want)
	}
	if got, want := createResponse.RestoreStatus.Phase, "bootstrapping"; got != want {
		t.Fatalf("createResponse.RestoreStatus.Phase = %q, want %q", got, want)
	}
	if creator.calls != 1 {
		t.Fatalf("creator.calls = %d, want 1", creator.calls)
	}

	if err := watcherRef.Load().store.UpsertCluster(mainRestoreTargetCluster("team-b", "alpha-restore")); err != nil {
		t.Fatalf("UpsertCluster(team-b/alpha-restore) error: %v", err)
	}

	assertJSONEndpoint(t, "http://"+addr+"/api/clusters/team-b/alpha-restore/restore-status", http.StatusOK, func(body []byte) {
		var response api.RestoreStatusResponse
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatalf("unmarshal /api/clusters/team-b/alpha-restore/restore-status response: %v", err)
		}
		if got, want := response.Status.Phase, "ready"; got != want {
			t.Fatalf("response.Status.Phase = %q, want %q", got, want)
		}
		if response.Status.Timestamps.ReadyAt == nil {
			t.Fatalf("response.Status.Timestamps = %#v, want ready timestamp", response.Status.Timestamps)
		}
	})

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run() to stop")
	}

	logs := logBuffer.String()
	for _, fragment := range []string{
		"/api/clusters/team-a/alpha/restore",
		"/api/clusters/team-b/alpha-restore/restore-status",
		"restore create accepted",
	} {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected logs to contain %q, got %s", fragment, logs)
		}
	}
}

func fakeBootstrapRuntime(t *testing.T) func(k8s.RuntimeConfig) (*k8s.ClientBootstrap, error) {
	t.Helper()

	return func(cfg k8s.RuntimeConfig) (*k8s.ClientBootstrap, error) {
		normalized, err := cfg.Normalize()
		if err != nil {
			return nil, err
		}

		scheme, err := k8s.NewScheme()
		if err != nil {
			return nil, err
		}

		cacheOptions, err := k8s.BuildCacheOptions(normalized)
		if err != nil {
			return nil, err
		}

		return &k8s.ClientBootstrap{
			Config:       normalized,
			RESTConfig:   &rest.Config{Host: "https://example.invalid"},
			Scheme:       scheme,
			CacheOptions: cacheOptions,
		}, nil
	}
}

func waitForWatcherStart(t *testing.T, getWatcher func() *fakeRuntimeWatcher) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		watcher := getWatcher()
		if watcher != nil {
			select {
			case <-watcher.startedCh:
				return
			default:
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("timed out waiting for watcher to start")
}

func waitForScraperStart(t *testing.T, getScraper func() *fakeRuntimeScraper) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		scraper := getScraper()
		if scraper != nil {
			select {
			case <-scraper.startedCh:
				return
			default:
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("timed out waiting for scraper to start")
}

func waitForServerConstruction(t *testing.T, getServer func() *api.Server) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if getServer() != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("timed out waiting for server construction")
}

func waitForServerAddress(t *testing.T, getServer func() *api.Server) string {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		server := getServer()
		if server != nil {
			addr := server.Addr()
			if addr != "" && addr != "127.0.0.1:0" {
				if _, err := http.Get("http://" + addr + "/healthz"); err == nil {
					return addr
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("timed out waiting for server to become ready")
	return ""
}

func waitForLogContains(t *testing.T, buf *safeBuffer, want string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for log substring %q in %s", want, buf.String())
}

func assertJSONEndpoint(t *testing.T, url string, wantStatus int, assertBody func([]byte)) {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s response body: %v", url, err)
	}

	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s status = %d, want %d; body=%s", url, resp.StatusCode, wantStatus, string(body))
	}

	assertBody(body)
}

type fakeRuntimeWatcher struct {
	store        *store.Store
	seed         func(*store.Store) error
	releaseReady <-chan struct{}
	startedCh    chan struct{}
	readyCh      chan struct{}
	startOnce    sync.Once
	readyOnce    sync.Once
}

func newFakeRuntimeWatcher(st *store.Store, releaseReady <-chan struct{}, seed func(*store.Store) error) *fakeRuntimeWatcher {
	return &fakeRuntimeWatcher{
		store:        st,
		seed:         seed,
		releaseReady: releaseReady,
		startedCh:    make(chan struct{}),
		readyCh:      make(chan struct{}),
	}
}

func (w *fakeRuntimeWatcher) Start(ctx context.Context) error {
	w.startOnce.Do(func() {
		close(w.startedCh)
	})

	if w.seed != nil {
		if err := w.seed(w.store); err != nil {
			return err
		}
	}

	if w.releaseReady != nil {
		select {
		case <-w.releaseReady:
		case <-ctx.Done():
			return nil
		}
	}

	w.readyOnce.Do(func() {
		close(w.readyCh)
	})

	<-ctx.Done()
	return nil
}

func (w *fakeRuntimeWatcher) Ready() <-chan struct{} {
	return w.readyCh
}

type fakeRuntimeScraper struct {
	clusters     map[string]metrics.ClusterMetrics
	releaseReady <-chan struct{}
	startedCh    chan struct{}
	readyCh      chan struct{}
	startOnce    sync.Once
	readyOnce    sync.Once
}

func newFakeRuntimeScraper(releaseReady <-chan struct{}, clusters map[string]metrics.ClusterMetrics) *fakeRuntimeScraper {
	if clusters == nil {
		clusters = make(map[string]metrics.ClusterMetrics)
	}
	return &fakeRuntimeScraper{
		clusters:     clusters,
		releaseReady: releaseReady,
		startedCh:    make(chan struct{}),
		readyCh:      make(chan struct{}),
	}
}

func (s *fakeRuntimeScraper) Start(ctx context.Context) error {
	s.startOnce.Do(func() {
		close(s.startedCh)
	})

	if s.releaseReady != nil {
		select {
		case <-s.releaseReady:
		case <-ctx.Done():
			return nil
		}
	}

	s.readyOnce.Do(func() {
		close(s.readyCh)
	})

	<-ctx.Done()
	return nil
}

func (s *fakeRuntimeScraper) Ready() <-chan struct{} {
	return s.readyCh
}

func (s *fakeRuntimeScraper) GetClusterMetrics(namespace, name string) (*metrics.ClusterMetrics, bool) {
	cached, ok := s.clusters[metricsKey(namespace, name)]
	if !ok {
		return nil, false
	}
	copy := cached
	copy.Instances = append([]metrics.InstanceMetrics(nil), cached.Instances...)
	return &copy, true
}

func metricsKey(namespace, name string) string {
	return namespace + "/" + name
}

func mainTestCluster(namespace, name string) *cnpgv1.Cluster {
	createdAt := metav1.NewTime(time.Date(2026, time.March, 24, 12, 0, 0, 0, time.UTC))
	firstRecovery := metav1.NewTime(time.Date(2026, time.March, 24, 10, 0, 0, 0, time.UTC))
	lastBackup := metav1.NewTime(time.Date(2026, time.March, 24, 11, 30, 0, 0, time.UTC))
	return &cnpgv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: createdAt,
		},
		Spec: cnpgv1.ClusterSpec{
			Instances: 3,
			ImageName: "ghcr.io/cloudnative-pg/postgresql:16.3",
		},
		Status: cnpgv1.ClusterStatus{
			Phase:          "healthy",
			ReadyInstances: 3,
			CurrentPrimary: name + "-1",
			Image:          "ghcr.io/cloudnative-pg/postgresql:16.3",
			FirstRecoverabilityPointByMethod: map[cnpgv1.BackupMethod]metav1.Time{
				cnpgv1.BackupMethod("barmanObjectStore"): firstRecovery,
			},
			LastSuccessfulBackupByMethod: map[cnpgv1.BackupMethod]metav1.Time{
				cnpgv1.BackupMethod("barmanObjectStore"): lastBackup,
			},
		},
	}
}

func mainTestClusterWithBackupConfig(namespace, name string) *cnpgv1.Cluster {
	cluster := mainTestCluster(namespace, name)
	cluster.Spec.Backup = &cnpgv1.BackupConfiguration{
		Target: cnpgv1.BackupTargetStandby,
		BarmanObjectStore: &cnpgv1.BarmanObjectStoreConfiguration{
			DestinationPath: "s3://deckhand-test/backups",
			BarmanCredentials: cnpgv1.BarmanCredentials{
				AWS: &cnpgv1.S3Credentials{InheritFromIAMRole: true},
			},
		},
	}
	return cluster
}

func mainTestBackup(namespace, name, clusterName string) *cnpgv1.Backup {
	createdAt := metav1.NewTime(time.Date(2026, time.March, 24, 11, 0, 0, 0, time.UTC))
	stoppedAt := metav1.NewTime(time.Date(2026, time.March, 24, 11, 5, 0, 0, time.UTC))
	return &cnpgv1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: createdAt,
		},
		Spec: cnpgv1.BackupSpec{
			Cluster: cnpgv1.LocalObjectReference{Name: clusterName},
			Method:  cnpgv1.BackupMethod("barmanObjectStore"),
		},
		Status: cnpgv1.BackupStatus{
			Phase:     cnpgv1.BackupPhase("completed"),
			Method:    cnpgv1.BackupMethod("barmanObjectStore"),
			StartedAt: &createdAt,
			StoppedAt: &stoppedAt,
		},
	}
}

func mainRestoreTestBackup(namespace, name, clusterName string) *cnpgv1.Backup {
	backup := mainTestBackup(namespace, name, clusterName)
	backup.Status.BackupID = "20260324T110000"
	backup.Status.DestinationPath = "s3://deckhand/backups"
	backup.Status.ServerName = clusterName
	return backup
}

func mainRestoreTargetCluster(namespace, name string) *cnpgv1.Cluster {
	cluster := mainTestCluster(namespace, name)
	cluster.Status.Phase = "healthy"
	cluster.Status.PhaseReason = "ready"
	cluster.Status.ReadyInstances = cluster.Spec.Instances
	cluster.Status.CurrentPrimaryTimestamp = "2026-03-24T12:05:00Z"
	cluster.Status.Conditions = []metav1.Condition{{
		Type:               string(cnpgv1.ConditionClusterReady),
		Status:             metav1.ConditionTrue,
		Reason:             string(cnpgv1.ClusterReady),
		Message:            "cluster is ready",
		LastTransitionTime: metav1.NewTime(time.Date(2026, time.March, 24, 12, 10, 0, 0, time.UTC)),
	}}
	return cluster
}

func mainTestScheduledBackup(namespace, name, clusterName string) *cnpgv1.ScheduledBackup {
	createdAt := metav1.NewTime(time.Date(2026, time.March, 24, 10, 0, 0, 0, time.UTC))
	lastSchedule := metav1.NewTime(time.Date(2026, time.March, 24, 10, 0, 0, 0, time.UTC))
	nextSchedule := metav1.NewTime(time.Date(2026, time.March, 25, 10, 0, 0, 0, time.UTC))
	immediate := true
	return &cnpgv1.ScheduledBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: createdAt,
		},
		Spec: cnpgv1.ScheduledBackupSpec{
			Cluster:   cnpgv1.LocalObjectReference{Name: clusterName},
			Schedule:  "0 0 0 * * *",
			Method:    cnpgv1.BackupMethod("barmanObjectStore"),
			Immediate: &immediate,
		},
		Status: cnpgv1.ScheduledBackupStatus{
			LastScheduleTime: &lastSchedule,
			NextScheduleTime: &nextSchedule,
		},
	}
}

func mainTestMetrics(namespace, name string) metrics.ClusterMetrics {
	scrapedAt := time.Date(2026, time.March, 24, 13, 0, 0, 0, time.UTC)
	return metrics.ClusterMetrics{
		Namespace:     namespace,
		ClusterName:   name,
		OverallHealth: metrics.Warning,
		ScrapeError:   "alpha-2 scrape http://10.0.0.12:9187/metrics degraded",
		ScrapedAt:     scrapedAt,
		Instances: []metrics.InstanceMetrics{
			{
				PodName: "alpha-1",
				Connections: metrics.ConnectionMetrics{
					Active:         4,
					Idle:           6,
					Total:          10,
					MaxConnections: 100,
				},
				Replication: metrics.ReplicationMetrics{
					ReplicationLagSeconds: 2,
					IsWALReceiverUp:       true,
					StreamingReplicas:     1,
					ReplayLagBytes:        1024,
				},
				Disk: metrics.DiskMetrics{
					PVCCapacityBytes:  20 * 1024 * 1024 * 1024,
					DatabaseSizeBytes: 8 * 1024 * 1024 * 1024,
				},
				Health:    metrics.Healthy,
				ScrapedAt: scrapedAt,
			},
			{
				PodName:     "alpha-2",
				Health:      metrics.Unknown,
				ScrapeError: "scrape http://10.0.0.12:9187/metrics: connection refused",
				ScrapedAt:   scrapedAt,
			},
		},
	}
}

// safeBuffer is a goroutine-safe bytes.Buffer for capturing slog output
// without triggering data races between the logging goroutine (run()) and the
// test goroutine that reads the buffer to verify log content.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fakeRuntimeBackupCreator struct {
	mu          sync.Mutex
	calls       int
	lastOptions api.BackupCreateOptions
	create      func(context.Context, *cnpgv1.Cluster, api.BackupCreateOptions) (*cnpgv1.Backup, error)
}

func (f *fakeRuntimeBackupCreator) CreateBackup(ctx context.Context, cluster *cnpgv1.Cluster, options api.BackupCreateOptions) (*cnpgv1.Backup, error) {
	f.mu.Lock()
	f.calls++
	f.lastOptions = options
	create := f.create
	f.mu.Unlock()
	if create == nil {
		return nil, nil
	}
	return create(ctx, cluster, options)
}

type fakeRuntimeRestoreCreator struct {
	mu          sync.Mutex
	calls       int
	lastOptions api.RestoreCreateOptions
	create      func(context.Context, *cnpgv1.Cluster, *cnpgv1.Backup, api.RestoreCreateOptions) (*cnpgv1.Cluster, error)
}

func (f *fakeRuntimeRestoreCreator) CreateCluster(ctx context.Context, sourceCluster *cnpgv1.Cluster, backup *cnpgv1.Backup, options api.RestoreCreateOptions) (*cnpgv1.Cluster, error) {
	f.mu.Lock()
	f.calls++
	f.lastOptions = options
	create := f.create
	f.mu.Unlock()
	if create == nil {
		return nil, nil
	}
	return create(ctx, sourceCluster, backup, options)
}

var _ runtimeWatcher = (*fakeRuntimeWatcher)(nil)
var _ runtimeMetricsScraper = (*fakeRuntimeScraper)(nil)
var _ api.BackupCreator = (*fakeRuntimeBackupCreator)(nil)
var _ api.RestoreCreator = (*fakeRuntimeRestoreCreator)(nil)

func TestMainServesEmbeddedApp(t *testing.T) {
	var logBuffer safeBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var serverRef atomic.Pointer[api.Server]

	deps := defaultAppDeps()
	deps.bootstrapRuntime = fakeBootstrapRuntime(t)
	deps.newWatcher = func(_ *k8s.ClientBootstrap, st *store.Store, _ *slog.Logger) (runtimeWatcher, error) {
		return newFakeRuntimeWatcher(st, nil, nil), nil
	}
	deps.newScraper = func(_ *k8s.ClientBootstrap, _ *store.Store, _ *slog.Logger) (runtimeMetricsScraper, error) {
		return newFakeRuntimeScraper(nil, nil), nil
	}
	deps.newServer = func(addr string, handler http.Handler, logger *slog.Logger) *api.Server {
		s := api.NewServer(addr, handler, logger)
		serverRef.Store(s)
		return s
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, k8s.RuntimeConfig{ListenAddr: "127.0.0.1:0"}, logger, deps)
	}()

	addr := waitForServerAddress(t, func() *api.Server { return serverRef.Load() })

	// Root should serve HTML (either the built SPA or the placeholder).
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET / error: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body=%s", resp.StatusCode, http.StatusOK, string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET / Content-Type = %q, want text/html prefix", ct)
	}
	if !strings.Contains(string(body), "Deckhand") {
		t.Fatalf("GET / body does not mention Deckhand: %s", string(body))
	}

	// Frontend-only route should fall back to index.html.
	resp2, err := http.Get("http://" + addr + "/clusters/team-a/alpha")
	if err != nil {
		t.Fatalf("GET /clusters/team-a/alpha error: %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /clusters/team-a/alpha status = %d, want %d; body=%s", resp2.StatusCode, http.StatusOK, string(body2))
	}
	if ct := resp2.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET /clusters/team-a/alpha Content-Type = %q, want text/html prefix", ct)
	}

	// API route should still return JSON, not HTML.
	assertJSONEndpoint(t, "http://"+addr+"/api/", http.StatusOK, func(body []byte) {
		if !strings.Contains(string(body), `"version":"0.1.0"`) {
			t.Fatalf("GET /api/ body does not contain version: %s", string(body))
		}
	})

	// Missing API route should return JSON 404, not SPA fallback.
	resp3, err := http.Get("http://" + addr + "/api/nonexistent")
	if err != nil {
		t.Fatalf("GET /api/nonexistent error: %v", err)
	}
	defer resp3.Body.Close()
	body3, _ := io.ReadAll(resp3.Body)

	if resp3.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/nonexistent status = %d, want %d; body=%s", resp3.StatusCode, http.StatusNotFound, string(body3))
	}
	if ct := resp3.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("GET /api/nonexistent Content-Type = %q, want application/json", ct)
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run() to stop")
	}

	logs := logBuffer.String()
	if !strings.Contains(logs, "embedded frontend ready") {
		t.Fatalf("expected logs to contain 'embedded frontend ready', got %s", logs)
	}
}
