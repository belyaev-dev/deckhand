package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/deckhand-for-cnpg/deckhand/internal/store"
)

func TestServeEmbeddedApp(t *testing.T) {
	indexHTML := []byte(`<!doctype html><html><head><title>Deckhand</title></head><body><div id="root"></div></body></html>`)
	jsContent := []byte(`console.log("app");`)

	distFS := fstest.MapFS{
		"index.html":            {Data: indexHTML},
		"assets/index-abc12.js": {Data: jsContent},
	}

	router := NewRouter(ServerDeps{
		Store: store.New(),
		EmbeddedApp: &EmbeddedApp{
			FS:        distFS,
			IndexHTML: indexHTML,
		},
	})

	t.Run("root path serves index.html", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET / status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("GET / Content-Type = %q, want text/html prefix", ct)
		}
		if !strings.Contains(rec.Body.String(), "<title>Deckhand</title>") {
			t.Fatalf("GET / body does not contain expected title tag: %s", rec.Body.String())
		}
	})

	t.Run("known asset path serves the real file", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/assets/index-abc12.js", nil)
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /assets/index-abc12.js status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if got := rec.Body.String(); got != string(jsContent) {
			t.Fatalf("GET /assets/index-abc12.js body = %q, want %q", got, string(jsContent))
		}
	})

	t.Run("frontend route falls back to index.html", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/clusters/team-a/alpha", nil)
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /clusters/team-a/alpha status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("GET /clusters/team-a/alpha Content-Type = %q, want text/html prefix", ct)
		}
		if !strings.Contains(rec.Body.String(), "<div id=\"root\">") {
			t.Fatalf("GET /clusters/team-a/alpha body does not contain root div: %s", rec.Body.String())
		}
	})

	t.Run("missing asset returns 404 not index.html", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil)
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET /assets/missing.js status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "<div id=\"root\">") {
			t.Fatalf("GET /assets/missing.js served SPA fallback for a static asset path")
		}
	})

	t.Run("healthz still works alongside embedded app", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /healthz status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
			t.Fatalf("GET /healthz body does not contain ok status: %s", rec.Body.String())
		}
	})
}

func TestSPAFallbackDoesNotInterceptAPI(t *testing.T) {
	indexHTML := []byte(`<!doctype html><html><head><title>Deckhand</title></head><body><div id="root"></div></body></html>`)

	router := NewRouter(ServerDeps{
		Store: store.New(),
		EmbeddedApp: &EmbeddedApp{
			FS:        fstest.MapFS{"index.html": {Data: indexHTML}},
			IndexHTML: indexHTML,
		},
	})

	t.Run("existing API route returns JSON", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/clusters", nil)
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/clusters status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("GET /api/clusters Content-Type = %q, want application/json", ct)
		}
	})

	t.Run("missing API route returns JSON 404 not SPA fallback", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/nonexistent", nil)
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET /api/nonexistent status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("GET /api/nonexistent Content-Type = %q, want application/json", ct)
		}
		if strings.Contains(rec.Body.String(), "<div id=\"root\">") {
			t.Fatalf("GET /api/nonexistent served SPA fallback for an API path")
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Fatalf("GET /api/nonexistent body missing error field: %s", rec.Body.String())
		}
	})

	t.Run("API version endpoint still works", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/", nil)
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/ status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"version":"0.1.0"`) {
			t.Fatalf("GET /api/ body does not contain version: %s", rec.Body.String())
		}
	})
}

func TestServeWithoutEmbeddedApp(t *testing.T) {
	// When no EmbeddedApp is provided the router should still work for API routes.
	router := NewRouter(ServerDeps{Store: store.New()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}
