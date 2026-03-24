// Package api provides the HTTP server and REST handlers for Deckhand.
package api

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// ServerDeps holds injectable dependencies for the HTTP server.
type ServerDeps struct {
	Logger         *slog.Logger
	Store          *store.Store
	MetricsReader  metricsReader
	BackupCreator  BackupCreator
	RestoreCreator RestoreCreator
	WebSocketHub   wsHandler
	// EmbeddedApp holds the embedded SPA filesystem (dist/ subtree) and index
	// HTML content. When set, the router serves frontend assets at "/" and falls
	// back to index.html for non-API/non-WS frontend routes.
	EmbeddedApp *EmbeddedApp
}

// EmbeddedApp bundles the compiled frontend assets together with the pre-read
// index.html content used for SPA fallback routing.
type EmbeddedApp struct {
	// FS is the embedded dist/ subtree served by the file server.
	FS fs.FS
	// IndexHTML is the pre-read index.html content returned for frontend routes.
	IndexHTML []byte
}

// wsHandler captures the WebSocket HTTP surface needed by the router.
type wsHandler interface {
	ServeWS(http.ResponseWriter, *http.Request)
}

// NewRouter builds the chi router with middleware and Deckhand API routes.
func NewRouter(deps ServerDeps) chi.Router {
	logger := ensureLogger(deps.Logger)
	handlers := newClusterHandlers(logger, deps.Store, deps.MetricsReader, deps.BackupCreator, deps.RestoreCreator)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(slogMiddleware(logger))
	r.Use(middleware.Recoverer)

	// Health check — useful immediately for liveness probes.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})

	if deps.WebSocketHub != nil {
		r.Get("/ws", deps.WebSocketHub.ServeWS)
	}

	r.Route("/api", func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"version":"0.1.0"}`)
		})
		r.Get("/clusters", handlers.listClusters)
		r.Get("/clusters/{namespace}/{name}", handlers.getCluster)
		r.Get("/clusters/{namespace}/{name}/metrics", handlers.getClusterMetrics)
		r.Get("/clusters/{namespace}/{name}/backups", handlers.listClusterBackups)
		r.Post("/clusters/{namespace}/{name}/backups", handlers.createClusterBackup)
		r.Get("/clusters/{namespace}/{name}/restore", handlers.listClusterRestoreOptions)
		r.Post("/clusters/{namespace}/{name}/restore", handlers.createClusterRestore)
		r.Get("/clusters/{namespace}/{name}/restore-status", handlers.getRestoreStatus)

		// API sub-router returns JSON 404s so SPA fallback never intercepts API paths.
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, logger, http.StatusNotFound, ErrorResponse{
				Error: "route not found",
			})
		})
	})

	// Serve the embedded SPA when assets are provided.
	if deps.EmbeddedApp != nil && deps.EmbeddedApp.IndexHTML != nil {
		mountEmbeddedApp(r, logger, deps.EmbeddedApp)
	}

	return r
}

// mountEmbeddedApp serves built frontend assets and falls back to index.html
// for non-API, non-WS, non-asset frontend routes so client-side routing works.
func mountEmbeddedApp(r chi.Router, logger *slog.Logger, app *EmbeddedApp) {
	fileServer := http.FileServer(http.FS(app.FS))

	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		// Strip the leading slash for fs.FS path lookup.
		reqPath := strings.TrimPrefix(r.URL.Path, "/")

		// Try to serve the real static file first.
		if reqPath != "" {
			if f, err := app.FS.Open(reqPath); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// If the path looks like a static asset request (has a file extension),
		// return 404 instead of falling through to index.html.
		if ext := path.Ext(reqPath); ext != "" {
			http.NotFound(w, r)
			return
		}

		// SPA fallback: serve index.html for all frontend routes.
		logger.Debug("spa fallback", "path", r.URL.Path)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(app.IndexHTML)
	})
}

// Server wraps http.Server with Deckhand lifecycle management.
type Server struct {
	httpServer *http.Server
	logger     *slog.Logger

	mu       sync.RWMutex
	listener net.Listener
}

// NewServer creates a Server bound to the given address using the provided
// router. The server is not started until ListenAndServe is called.
func NewServer(addr string, handler http.Handler, logger *slog.Logger) *Server {
	return &Server{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		logger: ensureLogger(logger),
	}
}

// ListenAndServe starts the HTTP server. It blocks until the server is
// stopped or encounters a fatal error.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", s.httpServer.Addr, err)
	}
	return s.serve(ln)
}

// Shutdown gracefully shuts down the HTTP server with the given timeout.
func (s *Server) Shutdown(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	s.logger.Info("shutting down HTTP server", "addr", s.Addr())
	return s.httpServer.Shutdown(ctx)
}

// Addr returns the server's actual listener address after startup. If the
// server hasn't started yet, it returns the configured address.
func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.httpServer.Addr
}

// ListenAndServeOnFreePort starts the server on an OS-assigned port and
// returns the actual address. Useful for tests.
func (s *Server) ListenAndServeOnFreePort() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen on free port: %w", err)
	}
	addr := ln.Addr().String()
	go func() {
		if serveErr := s.serve(ln); serveErr != nil {
			s.logger.Error("HTTP server error", "error", serveErr)
		}
	}()
	return addr, nil
}

func (s *Server) serve(ln net.Listener) error {
	s.setListener(ln)
	s.logger.Info("starting HTTP server", "addr", ln.Addr().String())
	err := s.httpServer.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) setListener(ln net.Listener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listener = ln
}

// slogMiddleware returns a chi middleware that logs each request using slog.
func slogMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	logger = ensureLogger(logger)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}

func ensureLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
