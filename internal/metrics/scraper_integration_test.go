//go:build integration

package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/k8s"
	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestScraperLiveCNPGPod(t *testing.T) {
	kubeconfig := os.Getenv("KUBECONFIG")
	namespace := os.Getenv("DECKHAND_TEST_NAMESPACE")
	clusterName := os.Getenv("DECKHAND_TEST_CLUSTER")
	if kubeconfig == "" || namespace == "" || clusterName == "" {
		t.Skip("set KUBECONFIG, DECKHAND_TEST_NAMESPACE, and DECKHAND_TEST_CLUSTER to run live CNPG scrape proof")
	}

	bootstrap, err := k8s.Bootstrap(k8s.RuntimeConfig{
		ListenAddr: ":8080",
		Kubeconfig: kubeconfig,
		Namespaces: []string{namespace},
	})
	if err != nil {
		t.Fatalf("k8s.Bootstrap() error: %v", err)
	}

	client, err := k8s.NewClient(bootstrap)
	if err != nil {
		t.Fatalf("k8s.NewClient() error: %v", err)
	}

	liveCluster := &cnpgv1.Cluster{}
	if err := client.Get(context.Background(), ctrlclient.ObjectKey{Namespace: namespace, Name: clusterName}, liveCluster); err != nil {
		t.Fatalf("get cluster %s/%s: %v", namespace, clusterName, err)
	}
	if liveCluster.IsMetricsTLSEnabled() {
		t.Skip("live cluster enables metrics TLS; T02 scraper currently proves direct HTTP pod scraping only")
	}
	if len(liveCluster.Status.InstanceNames) == 0 {
		t.Fatalf("cluster %s/%s has no instance names", namespace, clusterName)
	}

	podName := liveCluster.Status.InstanceNames[0]
	if pvcName, err := selectInstancePVC(liveCluster, podName); err != nil || pvcName == "" {
		t.Skipf("cluster %s/%s does not expose a matching healthy PVC for pod %s: %v", namespace, clusterName, podName, err)
	}

	clusterCopy := liveCluster.DeepCopy()
	clusterCopy.Status.InstanceNames = []string{podName}
	clusterCopy.Status.HealthyPVC = []string{podName}

	st := store.New()
	if err := st.UpsertCluster(clusterCopy); err != nil {
		t.Fatalf("UpsertCluster() error: %v", err)
	}

	scraper, err := NewScraper(st, client, &http.Client{Timeout: 5 * time.Second}, slog.New(slog.NewJSONHandler(os.Stderr, nil)), time.Minute, DefaultThresholds())
	if err != nil {
		t.Fatalf("NewScraper() error: %v", err)
	}

	scraper.scrapeOnce(context.Background())

	cached, ok := scraper.GetClusterMetrics(namespace, clusterName)
	if !ok {
		t.Fatalf("GetClusterMetrics(%s, %s) found no cached metrics", namespace, clusterName)
	}
	if got, want := len(cached.Instances), 1; got != want {
		t.Fatalf("len(cached.Instances) = %d, want %d", got, want)
	}

	instance := cached.Instances[0]
	if instance.PodName != podName {
		t.Fatalf("instance.PodName = %q, want %q", instance.PodName, podName)
	}
	if instance.ScrapeError != "" {
		t.Fatalf("instance.ScrapeError = %q, want empty", instance.ScrapeError)
	}
	if instance.ScrapedAt.IsZero() {
		t.Fatal("instance.ScrapedAt is zero, want live scrape timestamp")
	}
	if instance.Disk.PVCCapacityBytes <= 0 {
		t.Fatalf("instance.Disk.PVCCapacityBytes = %d, want > 0", instance.Disk.PVCCapacityBytes)
	}
	if cached.OverallHealth == "" {
		t.Fatal("cached.OverallHealth is empty")
	}
}
