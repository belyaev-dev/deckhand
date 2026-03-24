package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/metrics"
	"github.com/deckhand-for-cnpg/deckhand/internal/store"
)

func TestMetrics(t *testing.T) {
	t.Run("returns cached metrics with redacted diagnostics and degraded instances intact", func(t *testing.T) {
		st := store.New()
		cluster := apiTestCluster("team-a", "alpha")
		cluster.Status.InstancesStatus = map[cnpgv1.PodStatus][]string{
			cnpgv1.PodHealthy: {"alpha-1"},
			cnpgv1.PodFailed:  {"alpha-2"},
		}
		if err := st.UpsertCluster(cluster); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}

		scrapedAt := time.Date(2026, time.March, 24, 13, 0, 0, 0, time.UTC)
		reader := fakeMetricsReader{clusters: map[string]metrics.ClusterMetrics{
			"team-a/alpha": {
				Namespace:     "team-a",
				ClusterName:   "alpha",
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
						PodName: "alpha-2",
						Disk: metrics.DiskMetrics{
							PVCCapacityBytes: 10 * 1024 * 1024 * 1024,
						},
						Health:      metrics.Unknown,
						ScrapeError: "scrape http://10.0.0.12:9187/metrics: connection refused",
						ScrapedAt:   scrapedAt,
					},
				},
			},
		}}

		router := NewRouter(ServerDeps{Store: st, MetricsReader: reader})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/clusters/team-a/alpha/metrics", nil)

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /api/clusters/team-a/alpha/metrics status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want %q", got, "application/json")
		}

		var response ClusterMetricsResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(metrics response): %v", err)
		}
		if response.Cluster.Namespace != "team-a" || response.Cluster.Name != "alpha" {
			t.Fatalf("response.Cluster = %#v, want namespace=%q name=%q", response.Cluster, "team-a", "alpha")
		}
		if got, want := response.OverallHealth, "warning"; got != want {
			t.Fatalf("response.OverallHealth = %q, want %q", got, want)
		}
		if response.ScrapedAt == nil || !response.ScrapedAt.Equal(scrapedAt) {
			t.Fatalf("response.ScrapedAt = %#v, want %s", response.ScrapedAt, scrapedAt.Format(time.RFC3339))
		}
		if got := len(response.Instances); got != 2 {
			t.Fatalf("len(response.Instances) = %d, want 2", got)
		}
		if got, want := response.Instances[0].Connections.Total, 10; got != want {
			t.Fatalf("response.Instances[0].Connections.Total = %d, want %d", got, want)
		}
		if got, want := response.Instances[0].PodStatus, "healthy"; got != want {
			t.Fatalf("response.Instances[0].PodStatus = %q, want %q", got, want)
		}
		if got, want := response.Instances[1].Health, "unknown"; got != want {
			t.Fatalf("response.Instances[1].Health = %q, want %q", got, want)
		}
		if got, want := response.Instances[1].PodStatus, "failed"; got != want {
			t.Fatalf("response.Instances[1].PodStatus = %q, want %q", got, want)
		}
		if !strings.Contains(response.Instances[1].ScrapeError, "connection refused") {
			t.Fatalf("response.Instances[1].ScrapeError = %q, want connection error", response.Instances[1].ScrapeError)
		}
		for _, forbidden := range []string{"10.0.0.12", "cnpg_backends_total"} {
			if strings.Contains(recorder.Body.String(), forbidden) {
				t.Fatalf("response leaked forbidden fragment %q: %s", forbidden, recorder.Body.String())
			}
		}
	})

	t.Run("returns unknown empty metrics when cache is missing but cluster exists", func(t *testing.T) {
		st := store.New()
		if err := st.UpsertCluster(apiTestCluster("team-a", "alpha")); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}

		router := NewRouter(ServerDeps{Store: st, MetricsReader: fakeMetricsReader{clusters: map[string]metrics.ClusterMetrics{}}})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/clusters/team-a/alpha/metrics", nil)

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /api/clusters/team-a/alpha/metrics status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
		}

		var response ClusterMetricsResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(metrics response): %v", err)
		}
		if got, want := response.OverallHealth, "unknown"; got != want {
			t.Fatalf("response.OverallHealth = %q, want %q", got, want)
		}
		if got, want := response.ScrapeError, "metrics not available yet"; got != want {
			t.Fatalf("response.ScrapeError = %q, want %q", got, want)
		}
		if len(response.Instances) != 0 {
			t.Fatalf("len(response.Instances) = %d, want 0", len(response.Instances))
		}
	})

	t.Run("returns explicit 404 when cluster is missing", func(t *testing.T) {
		router := NewRouter(ServerDeps{Store: store.New(), MetricsReader: fakeMetricsReader{clusters: map[string]metrics.ClusterMetrics{}}})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/clusters/team-a/missing/metrics", nil)

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET /api/clusters/team-a/missing/metrics status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
		}

		var response ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(error response): %v", err)
		}
		wantError := `cluster "missing" in namespace "team-a" not found`
		if response.Error != wantError {
			t.Fatalf("response.Error = %q, want %q", response.Error, wantError)
		}
	})
}

type fakeMetricsReader struct {
	clusters map[string]metrics.ClusterMetrics
}

func (r fakeMetricsReader) GetClusterMetrics(namespace, name string) (*metrics.ClusterMetrics, bool) {
	snapshot, ok := r.clusters[fmt.Sprintf("%s/%s", namespace, name)]
	if !ok {
		return nil, false
	}
	copy := snapshot
	copy.Instances = append([]metrics.InstanceMetrics(nil), snapshot.Instances...)
	return &copy, true
}
