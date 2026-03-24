// Deckhand — Day-2 PostgreSQL operations dashboard for CloudNativePG.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/deckhand-for-cnpg/deckhand/internal/api"
	"github.com/deckhand-for-cnpg/deckhand/internal/k8s"
	"github.com/deckhand-for-cnpg/deckhand/internal/metrics"
	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	"github.com/deckhand-for-cnpg/deckhand/web"
)

func main() {
	logger := newLogger(os.Stdout)

	cfg, err := parseFlags(os.Args[1:], os.Getenv)
	if err != nil {
		logger.Error("invalid runtime config", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, logger, defaultAppDeps()); err != nil {
		logger.Error("deckhand exited with error", "error", err)
		os.Exit(1)
	}
}

type runtimeWatcher interface {
	Start(context.Context) error
	Ready() <-chan struct{}
}

type runtimeMetricsScraper interface {
	Start(context.Context) error
	Ready() <-chan struct{}
	GetClusterMetrics(namespace, name string) (*metrics.ClusterMetrics, bool)
}

type runtimeWebSocketHub interface {
	Start(context.Context) error
	Ready() <-chan struct{}
	ServeWS(http.ResponseWriter, *http.Request)
}

type appDeps struct {
	bootstrapRuntime  func(k8s.RuntimeConfig) (*k8s.ClientBootstrap, error)
	newStore          func() *store.Store
	newWatcher        func(*k8s.ClientBootstrap, *store.Store, *slog.Logger) (runtimeWatcher, error)
	newScraper        func(*k8s.ClientBootstrap, *store.Store, *slog.Logger) (runtimeMetricsScraper, error)
	newBackupCreator  func(*k8s.ClientBootstrap, *slog.Logger) (api.BackupCreator, error)
	newRestoreCreator func(*k8s.ClientBootstrap, *slog.Logger) (api.RestoreCreator, error)
	newWebSocketHub   func(*store.Store, *slog.Logger) runtimeWebSocketHub
	newRouter         func(api.ServerDeps) http.Handler
	newServer         func(string, http.Handler, *slog.Logger) *api.Server
}

func defaultAppDeps() appDeps {
	return appDeps{
		bootstrapRuntime: k8s.Bootstrap,
		newStore:         store.New,
		newWatcher: func(bootstrap *k8s.ClientBootstrap, st *store.Store, logger *slog.Logger) (runtimeWatcher, error) {
			return k8s.NewWatcher(bootstrap, st, logger)
		},
		newScraper: func(bootstrap *k8s.ClientBootstrap, st *store.Store, logger *slog.Logger) (runtimeMetricsScraper, error) {
			client, err := k8s.NewClient(bootstrap)
			if err != nil {
				return nil, err
			}
			return metrics.NewScraper(st, client, nil, logger, 0, metrics.HealthThresholds{})
		},
		newBackupCreator: func(bootstrap *k8s.ClientBootstrap, logger *slog.Logger) (api.BackupCreator, error) {
			return k8s.NewBackupCreator(bootstrap, logger)
		},
		newRestoreCreator: func(bootstrap *k8s.ClientBootstrap, logger *slog.Logger) (api.RestoreCreator, error) {
			return k8s.NewRestoreCreator(bootstrap, logger)
		},
		newWebSocketHub: func(st *store.Store, logger *slog.Logger) runtimeWebSocketHub {
			return api.NewWSHub(logger, st)
		},
		newRouter: func(deps api.ServerDeps) http.Handler {
			return api.NewRouter(deps)
		},
		newServer: api.NewServer,
	}
}

// parseFlags reads CLI flags and environment variables into a validated
// RuntimeConfig.
func parseFlags(args []string, getenv func(string) string) (k8s.RuntimeConfig, error) {
	fs := flag.NewFlagSet("deckhand", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var cfg k8s.RuntimeConfig
	var namespaces string

	fs.StringVar(&cfg.ListenAddr, "listen", envOrDefault(getenv, "DECKHAND_LISTEN", ":8080"), "HTTP listen address")
	fs.StringVar(&cfg.Kubeconfig, "kubeconfig", envOrDefault(getenv, "KUBECONFIG", ""), "path to kubeconfig (empty = in-cluster)")
	fs.StringVar(&namespaces, "namespaces", envOrDefault(getenv, "DECKHAND_NAMESPACES", ""), "comma-separated namespace list (empty = all)")

	if err := fs.Parse(args); err != nil {
		return k8s.RuntimeConfig{}, fmt.Errorf("parsing flags: %w", err)
	}

	cfg.Namespaces = k8s.ParseNamespaces(namespaces)

	normalized, err := cfg.Normalize()
	if err != nil {
		return k8s.RuntimeConfig{}, fmt.Errorf("normalizing runtime config: %w", err)
	}

	return normalized, nil
}

// run is the real entrypoint — separated from main() so tests can call it with
// a cancelable context and injectable dependencies without hitting os.Exit.
func run(ctx context.Context, cfg k8s.RuntimeConfig, logger *slog.Logger, deps appDeps) error {
	logger = apiLogger(logger)

	normalized, err := cfg.Normalize()
	if err != nil {
		logger.Error("invalid runtime config", "error", err)
		return fmt.Errorf("invalid runtime config: %w", err)
	}

	logger.Info("deckhand starting",
		"listen", normalized.ListenAddr,
		"namespace_scope", normalized.ScopeDescription(),
		"all_namespaces", normalized.AllNamespaces(),
		"has_kubeconfig", normalized.Kubeconfig != "",
	)

	bootstrap, err := deps.bootstrapRuntime(normalized)
	if err != nil {
		logger.Error("kubernetes bootstrap failed", "error", err)
		return fmt.Errorf("bootstrap kubernetes runtime: %w", err)
	}

	logger.Info("kubernetes runtime ready",
		"namespace_scope", bootstrap.Config.ScopeDescription(),
		"all_namespaces", bootstrap.Config.AllNamespaces(),
		"registered_types", len(bootstrap.Scheme.AllKnownTypes()),
	)

	runtimeStore := deps.newStore()
	if runtimeStore == nil {
		return errors.New("build runtime dependencies: store is required")
	}

	watcher, err := deps.newWatcher(bootstrap, runtimeStore, logger)
	if err != nil {
		logger.Error("cnpg watcher bootstrap failed", "error", err)
		return fmt.Errorf("bootstrap cnpg watcher: %w", err)
	}
	if watcher == nil {
		return errors.New("build runtime dependencies: watcher is required")
	}

	scraper, err := deps.newScraper(bootstrap, runtimeStore, logger)
	if err != nil {
		logger.Error("metrics scraper bootstrap failed", "error", err)
		return fmt.Errorf("bootstrap metrics scraper: %w", err)
	}
	if scraper == nil {
		return errors.New("build runtime dependencies: metrics scraper is required")
	}

	webSocketHub := deps.newWebSocketHub(runtimeStore, logger)
	if webSocketHub == nil {
		return errors.New("build runtime dependencies: websocket hub is required")
	}

	if deps.newBackupCreator == nil {
		return errors.New("build runtime dependencies: backup creator factory is required")
	}
	backupCreator, err := deps.newBackupCreator(bootstrap, logger)
	if err != nil {
		logger.Error("backup creator bootstrap failed", "error", err)
		return fmt.Errorf("bootstrap backup creator: %w", err)
	}
	if backupCreator == nil {
		return errors.New("build runtime dependencies: backup creator is required")
	}

	if deps.newRestoreCreator == nil {
		return errors.New("build runtime dependencies: restore creator factory is required")
	}
	restoreCreator, err := deps.newRestoreCreator(bootstrap, logger)
	if err != nil {
		logger.Error("restore creator bootstrap failed", "error", err)
		return fmt.Errorf("bootstrap restore creator: %w", err)
	}
	if restoreCreator == nil {
		return errors.New("build runtime dependencies: restore creator is required")
	}

	indexHTML, indexSource, indexErr := web.ReadIndexHTML()
	if indexErr != nil {
		logger.Warn("embedded frontend index.html not available", "error", indexErr)
	} else {
		logger.Info("embedded frontend ready", "source", indexSource)
	}

	var embeddedApp *api.EmbeddedApp
	if indexErr == nil {
		embeddedApp = &api.EmbeddedApp{
			FS:        web.DistFS(),
			IndexHTML: indexHTML,
		}
	}

	router := deps.newRouter(api.ServerDeps{Logger: logger, Store: runtimeStore, MetricsReader: scraper, BackupCreator: backupCreator, RestoreCreator: restoreCreator, WebSocketHub: webSocketHub, EmbeddedApp: embeddedApp})
	if router == nil {
		return errors.New("build runtime dependencies: router is required")
	}

	server := deps.newServer(normalized.ListenAddr, router, logger)
	if server == nil {
		return errors.New("build runtime dependencies: server is required")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	watcherErrCh := make(chan error, 1)
	go func() {
		watcherErrCh <- watcher.Start(runCtx)
	}()

	logger.Info("waiting for cnpg watcher readiness")

	watcherExited := false
	select {
	case <-watcher.Ready():
		logger.Info("cnpg watcher ready; starting metrics scraper")
	case err := <-watcherErrCh:
		watcherExited = true
		if err != nil {
			logger.Error("cnpg watcher failed before metrics scraper startup", "error", err)
			return fmt.Errorf("start cnpg watcher: %w", err)
		}
		logger.Error("cnpg watcher stopped before metrics scraper startup")
		return errors.New("start cnpg watcher: watcher stopped before metrics scraper startup")
	case <-ctx.Done():
		logger.Info("shutdown requested", "reason", ctx.Err())
		cancel()
		if !watcherExited {
			if err := <-watcherErrCh; err != nil {
				logger.Error("cnpg watcher stopped with error", "error", err)
				return fmt.Errorf("run cnpg watcher: %w", err)
			}
		}
		logger.Info("deckhand stopped")
		return nil
	}

	scraperErrCh := make(chan error, 1)
	go func() {
		scraperErrCh <- scraper.Start(runCtx)
	}()

	logger.Info("waiting for metrics scraper readiness")

	scraperExited := false
	select {
	case <-scraper.Ready():
		logger.Info("metrics scraper ready; starting websocket hub")
	case err := <-scraperErrCh:
		scraperExited = true
		if err != nil {
			logger.Error("metrics scraper failed before websocket hub startup", "error", err)
			return fmt.Errorf("start metrics scraper: %w", err)
		}
		logger.Error("metrics scraper stopped before websocket hub startup")
		return errors.New("start metrics scraper: scraper stopped before websocket hub startup")
	case err := <-watcherErrCh:
		watcherExited = true
		if err != nil {
			logger.Error("cnpg watcher stopped before metrics scraper readiness", "error", err)
			return fmt.Errorf("run cnpg watcher: %w", err)
		}
		logger.Error("cnpg watcher stopped before metrics scraper readiness")
		return errors.New("run cnpg watcher: watcher stopped before metrics scraper readiness")
	case <-ctx.Done():
		logger.Info("shutdown requested", "reason", ctx.Err())
		cancel()
		if !scraperExited {
			if err := <-scraperErrCh; err != nil {
				logger.Error("metrics scraper stopped with error", "error", err)
				return fmt.Errorf("run metrics scraper: %w", err)
			}
		}
		if !watcherExited {
			if err := <-watcherErrCh; err != nil {
				logger.Error("cnpg watcher stopped with error", "error", err)
				return fmt.Errorf("run cnpg watcher: %w", err)
			}
		}
		logger.Info("deckhand stopped")
		return nil
	}

	hubErrCh := make(chan error, 1)
	go func() {
		hubErrCh <- webSocketHub.Start(runCtx)
	}()

	logger.Info("waiting for websocket hub readiness")

	hubExited := false
	select {
	case <-webSocketHub.Ready():
		logger.Info("websocket hub ready; starting HTTP server")
	case err := <-hubErrCh:
		hubExited = true
		if err != nil {
			logger.Error("websocket hub failed before API startup", "error", err)
			return fmt.Errorf("start websocket hub: %w", err)
		}
		logger.Error("websocket hub stopped before API startup")
		return errors.New("start websocket hub: websocket hub stopped before API startup")
	case err := <-scraperErrCh:
		scraperExited = true
		if err != nil {
			logger.Error("metrics scraper stopped before websocket hub readiness", "error", err)
			return fmt.Errorf("run metrics scraper: %w", err)
		}
		logger.Error("metrics scraper stopped before websocket hub readiness")
		return errors.New("run metrics scraper: scraper stopped before websocket hub readiness")
	case err := <-watcherErrCh:
		watcherExited = true
		if err != nil {
			logger.Error("cnpg watcher stopped before websocket hub readiness", "error", err)
			return fmt.Errorf("run cnpg watcher: %w", err)
		}
		logger.Error("cnpg watcher stopped before websocket hub readiness")
		return errors.New("run cnpg watcher: watcher stopped before websocket hub readiness")
	case <-ctx.Done():
		logger.Info("shutdown requested", "reason", ctx.Err())
		cancel()
		if !hubExited {
			if err := <-hubErrCh; err != nil {
				logger.Error("websocket hub stopped with error", "error", err)
				return fmt.Errorf("run websocket hub: %w", err)
			}
		}
		if !scraperExited {
			if err := <-scraperErrCh; err != nil {
				logger.Error("metrics scraper stopped with error", "error", err)
				return fmt.Errorf("run metrics scraper: %w", err)
			}
		}
		if !watcherExited {
			if err := <-watcherErrCh; err != nil {
				logger.Error("cnpg watcher stopped with error", "error", err)
				return fmt.Errorf("run cnpg watcher: %w", err)
			}
		}
		logger.Info("deckhand stopped")
		return nil
	}

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- server.ListenAndServe()
	}()

	serverExited := false
	var runErr error

	select {
	case err := <-serverErrCh:
		serverExited = true
		if err != nil {
			logger.Error("HTTP server failed", "error", err)
			runErr = fmt.Errorf("serve HTTP server: %w", err)
		}
	case err := <-hubErrCh:
		hubExited = true
		if err != nil {
			logger.Error("websocket hub stopped with error", "error", err)
			runErr = fmt.Errorf("run websocket hub: %w", err)
		} else {
			logger.Error("websocket hub stopped unexpectedly")
			runErr = errors.New("run websocket hub: websocket hub stopped unexpectedly")
		}
	case err := <-watcherErrCh:
		watcherExited = true
		if err != nil {
			logger.Error("cnpg watcher stopped with error", "error", err)
			runErr = fmt.Errorf("run cnpg watcher: %w", err)
		} else {
			logger.Error("cnpg watcher stopped unexpectedly")
			runErr = errors.New("run cnpg watcher: watcher stopped unexpectedly")
		}
	case err := <-scraperErrCh:
		scraperExited = true
		if err != nil {
			logger.Error("metrics scraper stopped with error", "error", err)
			runErr = fmt.Errorf("run metrics scraper: %w", err)
		} else {
			logger.Error("metrics scraper stopped unexpectedly")
			runErr = errors.New("run metrics scraper: scraper stopped unexpectedly")
		}
	case <-ctx.Done():
		logger.Info("shutdown requested", "reason", ctx.Err())
	}

	cancel()

	if err := server.Shutdown(10 * time.Second); err != nil {
		logger.Error("HTTP server shutdown failed", "error", err)
		if runErr == nil {
			runErr = fmt.Errorf("shutdown HTTP server: %w", err)
		}
	}

	if !serverExited {
		if err := <-serverErrCh; err != nil && runErr == nil {
			logger.Error("HTTP server failed", "error", err)
			runErr = fmt.Errorf("serve HTTP server: %w", err)
		}
	}

	if !hubExited {
		if err := <-hubErrCh; err != nil && runErr == nil {
			logger.Error("websocket hub stopped with error", "error", err)
			runErr = fmt.Errorf("run websocket hub: %w", err)
		}
	}

	if !scraperExited {
		if err := <-scraperErrCh; err != nil && runErr == nil {
			logger.Error("metrics scraper stopped with error", "error", err)
			runErr = fmt.Errorf("run metrics scraper: %w", err)
		}
	}

	if !watcherExited {
		if err := <-watcherErrCh; err != nil && runErr == nil {
			logger.Error("cnpg watcher stopped with error", "error", err)
			runErr = fmt.Errorf("run cnpg watcher: %w", err)
		}
	}

	if runErr != nil {
		return runErr
	}

	logger.Info("deckhand stopped")
	return nil
}

func envOrDefault(getenv func(string) string, key, fallback string) string {
	if getenv != nil {
		if value := getenv(key); value != "" {
			return value
		}
	}
	return fallback
}

func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func apiLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return newLogger(io.Discard)
}
