package metrics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlclientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestScraperInitialSweepCachesMetricsAndSignalsReady(t *testing.T) {
	st := store.New()
	cluster := scraperTestCluster("team-a", "alpha", []string{"alpha-1"}, []string{"alpha-1"})
	if err := st.UpsertCluster(cluster); err != nil {
		t.Fatalf("UpsertCluster() error: %v", err)
	}

	client := newScraperTestClient(t,
		scraperTestPod("team-a", "alpha-1", "10.0.0.11"),
		scraperTestPVC("team-a", "alpha-1", "10Gi"),
	)

	var logs bytes.Buffer
	scraper := newScraperForTest(t, st, client, &http.Client{
		Timeout:   250 * time.Millisecond,
		Transport: roundTripFunc(singleMetricsResponse(t, "10.0.0.11", http.StatusOK, scraperTestMetrics(6, 0))),
	}, slog.New(slog.NewJSONHandler(&logs, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- scraper.Start(ctx)
	}()

	waitForScraperReady(t, scraper.Ready())

	cached, ok := scraper.GetClusterMetrics("team-a", "alpha")
	if !ok {
		t.Fatal("GetClusterMetrics() found no cached metrics")
	}
	if got, want := len(cached.Instances), 1; got != want {
		t.Fatalf("len(cached.Instances) = %d, want %d", got, want)
	}
	instance := cached.Instances[0]
	if instance.ScrapeError != "" {
		t.Fatalf("instance.ScrapeError = %q, want empty", instance.ScrapeError)
	}
	if instance.ScrapedAt.IsZero() {
		t.Fatal("instance.ScrapedAt is zero, want scrape timestamp")
	}
	if got, want := instance.Connections.Total, 6; got != want {
		t.Fatalf("Connections.Total = %d, want %d", got, want)
	}
	wantCapacity := resource.MustParse("10Gi")
	if got, want := instance.Disk.PVCCapacityBytes, wantCapacity.Value(); got != want {
		t.Fatalf("Disk.PVCCapacityBytes = %d, want %d", got, want)
	}
	if got, want := cached.OverallHealth, Healthy; got != want {
		t.Fatalf("cached.OverallHealth = %q, want %q", got, want)
	}
	if cached.ScrapedAt.IsZero() {
		t.Fatal("cached.ScrapedAt is zero, want cluster scrape timestamp")
	}
	if !strings.Contains(logs.String(), "metrics scraper ready") {
		t.Fatalf("expected logs to contain readiness transition, got %s", logs.String())
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("scraper.Start() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for scraper to stop")
	}
}

func TestScraperPreservesInstanceDiagnostics(t *testing.T) {
	st := store.New()
	cluster := scraperTestCluster("team-a", "alpha", []string{"alpha-1", "alpha-2", "alpha-3"}, []string{"alpha-1", "alpha-2"})
	if err := st.UpsertCluster(cluster); err != nil {
		t.Fatalf("UpsertCluster() error: %v", err)
	}

	client := newScraperTestClient(t,
		scraperTestPod("team-a", "alpha-1", "10.0.0.11"),
		scraperTestPod("team-a", "alpha-2", "10.0.0.12"),
		scraperTestPod("team-a", "alpha-3", "10.0.0.13"),
		scraperTestPVC("team-a", "alpha-1", "10Gi"),
		scraperTestPVC("team-a", "alpha-2", "20Gi"),
	)

	var logs bytes.Buffer
	scraper := newScraperForTest(t, st, client, &http.Client{
		Timeout: 250 * time.Millisecond,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got, want := req.URL.Port(), "9187"; got != want {
				return nil, fmt.Errorf("request port = %q, want %q", got, want)
			}

			switch req.URL.Hostname() {
			case "10.0.0.11":
				return httpResponse(http.StatusOK, scraperTestMetrics(9, 15)), nil
			case "10.0.0.12":
				return nil, fmt.Errorf("dial tcp 10.0.0.12:9187: connect: connection refused")
			case "10.0.0.13":
				return httpResponse(http.StatusOK, scraperTestMetrics(2, 0)), nil
			default:
				return nil, fmt.Errorf("unexpected scrape target %q", req.URL.Host)
			}
		}),
	}, slog.New(slog.NewJSONHandler(&logs, nil)))

	scraper.scrapeOnce(context.Background())

	cached, ok := scraper.GetClusterMetrics("team-a", "alpha")
	if !ok {
		t.Fatal("GetClusterMetrics() found no cached metrics")
	}
	if got, want := len(cached.Instances), 3; got != want {
		t.Fatalf("len(cached.Instances) = %d, want %d", got, want)
	}

	good, ok := scraper.GetInstanceMetrics("team-a", "alpha", "alpha-1")
	if !ok {
		t.Fatal("GetInstanceMetrics(alpha-1) found no metrics")
	}
	if good.ScrapeError != "" {
		t.Fatalf("alpha-1 ScrapeError = %q, want empty", good.ScrapeError)
	}
	wantCapacity := resource.MustParse("10Gi")
	if got, want := good.Disk.PVCCapacityBytes, wantCapacity.Value(); got != want {
		t.Fatalf("alpha-1 PVCCapacityBytes = %d, want %d", got, want)
	}
	if got, want := good.Health, Warning; got != want {
		t.Fatalf("alpha-1 Health = %q, want %q", got, want)
	}

	badScrape, ok := scraper.GetInstanceMetrics("team-a", "alpha", "alpha-2")
	if !ok {
		t.Fatal("GetInstanceMetrics(alpha-2) found no metrics")
	}
	if badScrape.ScrapeError == "" || !strings.Contains(badScrape.ScrapeError, "connection refused") {
		t.Fatalf("alpha-2 ScrapeError = %q, want connection error", badScrape.ScrapeError)
	}
	wantCapacity = resource.MustParse("20Gi")
	if got, want := badScrape.Disk.PVCCapacityBytes, wantCapacity.Value(); got != want {
		t.Fatalf("alpha-2 PVCCapacityBytes = %d, want %d", got, want)
	}
	if badScrape.ScrapedAt.IsZero() {
		t.Fatal("alpha-2 ScrapedAt is zero, want attempt timestamp")
	}
	if got, want := badScrape.Health, Unknown; got != want {
		t.Fatalf("alpha-2 Health = %q, want %q", got, want)
	}

	badPVC, ok := scraper.GetInstanceMetrics("team-a", "alpha", "alpha-3")
	if !ok {
		t.Fatal("GetInstanceMetrics(alpha-3) found no metrics")
	}
	if badPVC.ScrapeError == "" || !strings.Contains(badPVC.ScrapeError, "no healthy PVC matched pod name") {
		t.Fatalf("alpha-3 ScrapeError = %q, want PVC match diagnostic", badPVC.ScrapeError)
	}
	if got, want := badPVC.Connections.Total, 2; got != want {
		t.Fatalf("alpha-3 Connections.Total = %d, want %d", got, want)
	}
	if badPVC.ScrapedAt.IsZero() {
		t.Fatal("alpha-3 ScrapedAt is zero, want attempt timestamp")
	}
	if got, want := badPVC.Health, Unknown; got != want {
		t.Fatalf("alpha-3 Health = %q, want %q", got, want)
	}

	if got, want := cached.OverallHealth, Warning; got != want {
		t.Fatalf("cached.OverallHealth = %q, want %q", got, want)
	}
	for _, fragment := range []string{"metrics scrape warning", "alpha-2", "alpha-3", "scraped_at"} {
		if !strings.Contains(logs.String(), fragment) {
			t.Fatalf("expected logs to contain %q, got %s", fragment, logs.String())
		}
	}
}

func TestScraperClusterWithoutInstanceNamesProducesClusterDiagnostic(t *testing.T) {
	st := store.New()
	if err := st.UpsertCluster(scraperTestCluster("team-a", "alpha", nil, nil)); err != nil {
		t.Fatalf("UpsertCluster() error: %v", err)
	}

	var logs bytes.Buffer
	scraper := newScraperForTest(t, st, newScraperTestClient(t), &http.Client{Timeout: 250 * time.Millisecond, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected request for %s", req.URL.String())
	})}, slog.New(slog.NewJSONHandler(&logs, nil)))

	scraper.scrapeOnce(context.Background())

	cached, ok := scraper.GetClusterMetrics("team-a", "alpha")
	if !ok {
		t.Fatal("GetClusterMetrics() found no cached metrics")
	}
	if got, want := cached.ScrapeError, "cluster has no instance names to scrape"; got != want {
		t.Fatalf("cached.ScrapeError = %q, want %q", got, want)
	}
	if got, want := cached.OverallHealth, Unknown; got != want {
		t.Fatalf("cached.OverallHealth = %q, want %q", got, want)
	}
	if !strings.Contains(logs.String(), "cluster has no instance names to scrape") {
		t.Fatalf("expected logs to contain cluster diagnostic, got %s", logs.String())
	}
}

func newScraperForTest(t *testing.T, st *store.Store, client ctrlclient.Client, httpClient *http.Client, logger *slog.Logger) *Scraper {
	t.Helper()

	scraper, err := NewScraper(st, client, httpClient, logger, time.Hour, DefaultThresholds())
	if err != nil {
		t.Fatalf("NewScraper() error: %v", err)
	}
	return scraper
}

func newScraperTestClient(t *testing.T, objects ...runtime.Object) ctrlclient.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme.AddToScheme() error: %v", err)
	}
	if err := cnpgv1.AddToScheme(scheme); err != nil {
		t.Fatalf("cnpgv1.AddToScheme() error: %v", err)
	}

	builder := ctrlclientfake.NewClientBuilder().WithScheme(scheme)
	if len(objects) > 0 {
		builder = builder.WithRuntimeObjects(objects...)
	}
	return builder.Build()
}

func waitForScraperReady(t *testing.T, ready <-chan struct{}) {
	t.Helper()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for scraper readiness")
	}
}

func singleMetricsResponse(t *testing.T, wantHost string, status int, body string) func(*http.Request) (*http.Response, error) {
	t.Helper()
	return func(req *http.Request) (*http.Response, error) {
		if got, want := req.URL.Scheme, "http"; got != want {
			return nil, fmt.Errorf("request scheme = %q, want %q", got, want)
		}
		if got, want := req.URL.Hostname(), wantHost; got != want {
			return nil, fmt.Errorf("request host = %q, want %q", got, want)
		}
		if got, want := req.URL.Port(), "9187"; got != want {
			return nil, fmt.Errorf("request port = %q, want %q", got, want)
		}
		if got, want := req.URL.Path, "/metrics"; got != want {
			return nil, fmt.Errorf("request path = %q, want %q", got, want)
		}
		return httpResponse(status, body), nil
	}
}

func httpResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func scraperTestMetrics(totalConnections int, replicationLagSeconds float64) string {
	return fmt.Sprintf(`# TYPE cnpg_backends_total gauge
cnpg_backends_total{state="active"} %d
# TYPE cnpg_pg_replication_lag gauge
cnpg_pg_replication_lag %.1f
# TYPE cnpg_pg_replication_in_recovery gauge
cnpg_pg_replication_in_recovery 0
# TYPE cnpg_pg_replication_is_wal_receiver_up gauge
cnpg_pg_replication_is_wal_receiver_up 1
# TYPE cnpg_pg_replication_streaming_replicas gauge
cnpg_pg_replication_streaming_replicas 1
# TYPE cnpg_pg_stat_replication_replay_diff_bytes gauge
cnpg_pg_stat_replication_replay_diff_bytes{application_name="alpha-2"} 1024
`, totalConnections, replicationLagSeconds)
}

func scraperTestCluster(namespace, name string, instanceNames, healthyPVC []string) *cnpgv1.Cluster {
	return &cnpgv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       cnpgv1.ClusterSpec{Instances: len(instanceNames)},
		Status: cnpgv1.ClusterStatus{
			InstanceNames: append([]string(nil), instanceNames...),
			HealthyPVC:    append([]string(nil), healthyPVC...),
		},
	}
}

func scraperTestPod(namespace, name, podIP string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Status:     corev1.PodStatus{PodIP: podIP},
	}
}

func scraperTestPVC(namespace, name, capacity string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(capacity)},
		},
	}
}
