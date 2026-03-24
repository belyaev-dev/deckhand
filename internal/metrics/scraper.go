package metrics

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultScrapeInterval = 30 * time.Second
	defaultHTTPTimeout    = 5 * time.Second
	metricsPort           = 9187
)

// Reader exposes the cache-backed metrics read surface used by the API layer.
type Reader interface {
	Ready() <-chan struct{}
	GetClusterMetrics(namespace, name string) (*ClusterMetrics, bool)
	GetInstanceMetrics(namespace, clusterName, podName string) (*InstanceMetrics, bool)
}

// Scraper periodically fetches CloudNativePG per-pod exporter metrics and
// stores the latest typed snapshots in memory.
type Scraper struct {
	store      *store.Store
	client     ctrlclient.Client
	httpClient *http.Client
	logger     *slog.Logger
	interval   time.Duration
	thresholds HealthThresholds
	now        func() time.Time

	readyOnce sync.Once
	readyCh   chan struct{}

	mu        sync.RWMutex
	clusters  map[string]ClusterMetrics
	instances map[string]InstanceMetrics
}

// NewScraper constructs a cache-backed scraper with sensible runtime defaults.
func NewScraper(
	st *store.Store,
	client ctrlclient.Client,
	httpClient *http.Client,
	logger *slog.Logger,
	interval time.Duration,
	thresholds HealthThresholds,
) (*Scraper, error) {
	if st == nil {
		return nil, fmt.Errorf("metrics scraper store is required")
	}
	if client == nil {
		return nil, fmt.Errorf("metrics scraper kubernetes client is required")
	}
	if interval <= 0 {
		interval = defaultScrapeInterval
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	} else if httpClient.Timeout <= 0 {
		copy := *httpClient
		copy.Timeout = defaultHTTPTimeout
		httpClient = &copy
	}
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	if thresholds == (HealthThresholds{}) {
		thresholds = DefaultThresholds()
	}

	return &Scraper{
		store:      st,
		client:     client,
		httpClient: httpClient,
		logger:     logger,
		interval:   interval,
		thresholds: thresholds,
		now: func() time.Time {
			return time.Now().UTC()
		},
		readyCh:   make(chan struct{}),
		clusters:  make(map[string]ClusterMetrics),
		instances: make(map[string]InstanceMetrics),
	}, nil
}

// Start runs the initial sweep immediately, signals readiness after that first
// attempt, then continues scraping on the configured interval until ctx ends.
func (s *Scraper) Start(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("metrics scraper context is required")
	}

	s.logger.Info("starting metrics scraper", "interval", s.interval.String())
	s.scrapeOnce(ctx)
	s.readyOnce.Do(func() {
		s.logger.Info("metrics scraper ready")
		close(s.readyCh)
	})

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("metrics scraper stopped", "reason", ctx.Err())
			return nil
		case <-ticker.C:
			s.scrapeOnce(ctx)
		}
	}
}

// Ready closes after the initial scrape sweep completes.
func (s *Scraper) Ready() <-chan struct{} {
	return s.readyCh
}

// GetClusterMetrics returns a deep-copied cached cluster snapshot.
func (s *Scraper) GetClusterMetrics(namespace, name string) (*ClusterMetrics, bool) {
	if s == nil {
		return nil, false
	}

	key := clusterCacheKey(namespace, name)
	s.mu.RLock()
	cached, ok := s.clusters[key]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}

	copy := cloneClusterMetrics(cached)
	return &copy, true
}

// GetInstanceMetrics returns a deep-copied cached instance snapshot.
func (s *Scraper) GetInstanceMetrics(namespace, clusterName, podName string) (*InstanceMetrics, bool) {
	if s == nil {
		return nil, false
	}

	key := instanceCacheKey(namespace, clusterName, podName)
	s.mu.RLock()
	cached, ok := s.instances[key]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}

	copy := cached
	return &copy, true
}

func (s *Scraper) scrapeOnce(ctx context.Context) {
	clusters := s.store.ListClusters("")
	nextClusters := make(map[string]ClusterMetrics, len(clusters))
	nextInstances := make(map[string]InstanceMetrics)

	for _, cluster := range clusters {
		if cluster == nil {
			continue
		}

		clusterMetrics := s.scrapeCluster(ctx, cluster)
		nextClusters[clusterCacheKey(cluster.Namespace, cluster.Name)] = clusterMetrics
		for _, instance := range clusterMetrics.Instances {
			nextInstances[instanceCacheKey(cluster.Namespace, cluster.Name, instance.PodName)] = instance
		}
	}

	s.mu.Lock()
	s.clusters = nextClusters
	s.instances = nextInstances
	s.mu.Unlock()
}

func (s *Scraper) scrapeCluster(ctx context.Context, cluster *cnpgv1.Cluster) ClusterMetrics {
	scrapedAt := s.now()
	result := ClusterMetrics{
		Namespace:   cluster.Namespace,
		ClusterName: cluster.Name,
		ScrapedAt:   scrapedAt,
	}

	instanceNames := append([]string(nil), cluster.Status.InstanceNames...)
	sort.Strings(instanceNames)
	if len(instanceNames) == 0 {
		result.ScrapeError = "cluster has no instance names to scrape"
		result.OverallHealth = Unknown
		s.logger.Warn("metrics scrape warning",
			"namespace", cluster.Namespace,
			"cluster", cluster.Name,
			"error", result.ScrapeError,
		)
		return result
	}

	instances := make([]InstanceMetrics, 0, len(instanceNames))
	for _, podName := range instanceNames {
		instance := s.scrapeInstance(ctx, cluster, podName, scrapedAt)
		instances = append(instances, instance)
	}

	result.Instances = instances
	result.OverallHealth = AggregateClusterHealth(instances)
	return result
}

func (s *Scraper) scrapeInstance(ctx context.Context, cluster *cnpgv1.Cluster, podName string, scrapedAt time.Time) InstanceMetrics {
	instance := InstanceMetrics{PodName: podName, ScrapedAt: scrapedAt}
	errs := make([]string, 0, 2)

	podIP, err := s.lookupPodIP(ctx, cluster.Namespace, podName)
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		parsed, parseErr := s.fetchPodMetrics(ctx, podIP)
		if parseErr != nil {
			errs = append(errs, parseErr.Error())
		} else if parsed != nil {
			instance.Connections = parsed.Connections
			instance.Replication = parsed.Replication
			instance.Disk.DatabaseSizeBytes = parsed.Disk.DatabaseSizeBytes
		}
	}

	pvcName, pvcNameErr := selectInstancePVC(cluster, podName)
	if pvcNameErr != nil {
		errs = append(errs, pvcNameErr.Error())
	} else {
		capacityBytes, pvcErr := s.lookupPVCCapacity(ctx, cluster.Namespace, pvcName)
		if pvcErr != nil {
			errs = append(errs, pvcErr.Error())
		} else {
			instance.Disk.PVCCapacityBytes = capacityBytes
		}
	}

	instance.ScrapeError = joinScrapeErrors(errs)
	instance.Health = EvaluateHealth(&instance, s.thresholds)
	if instance.ScrapeError != "" {
		s.logger.Warn("metrics scrape warning",
			"namespace", cluster.Namespace,
			"cluster", cluster.Name,
			"pod", podName,
			"scraped_at", instance.ScrapedAt.Format(time.RFC3339),
			"error", instance.ScrapeError,
		)
	}

	return instance
}

func (s *Scraper) lookupPodIP(ctx context.Context, namespace, podName string) (string, error) {
	pod := &corev1.Pod{}
	if err := s.client.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: podName}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("resolve pod %s/%s: not found", namespace, podName)
		}
		return "", fmt.Errorf("resolve pod %s/%s: %w", namespace, podName, err)
	}

	podIP := strings.TrimSpace(pod.Status.PodIP)
	if podIP == "" {
		return "", fmt.Errorf("resolve pod %s/%s: pod IP is empty", namespace, podName)
	}
	return podIP, nil
}

func (s *Scraper) fetchPodMetrics(ctx context.Context, podIP string) (*InstanceMetrics, error) {
	requestCtx, cancel := context.WithTimeout(ctx, s.httpClient.Timeout)
	defer cancel()

	url := fmt.Sprintf("http://%s:%d/metrics", podIP, metricsPort)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build scrape request for %s: %w", podIP, err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scrape %s: unexpected status %d", url, resp.StatusCode)
	}

	parsed, err := ParseMetrics(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", url, err)
	}
	return parsed, nil
}

func (s *Scraper) lookupPVCCapacity(ctx context.Context, namespace, pvcName string) (int64, error) {
	pvc := &corev1.PersistentVolumeClaim{}
	if err := s.client.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: pvcName}, pvc); err != nil {
		if apierrors.IsNotFound(err) {
			return 0, fmt.Errorf("resolve PVC %s/%s: not found", namespace, pvcName)
		}
		return 0, fmt.Errorf("resolve PVC %s/%s: %w", namespace, pvcName, err)
	}

	capacity, ok := pvc.Status.Capacity[corev1.ResourceStorage]
	if !ok {
		return 0, fmt.Errorf("resolve PVC %s/%s: storage capacity is empty", namespace, pvcName)
	}

	return capacity.Value(), nil
}

func selectInstancePVC(cluster *cnpgv1.Cluster, podName string) (string, error) {
	if cluster == nil {
		return "", fmt.Errorf("cluster is required")
	}

	for _, pvcName := range cluster.Status.HealthyPVC {
		if pvcName == podName {
			return pvcName, nil
		}
	}

	if len(cluster.Status.HealthyPVC) == 0 {
		return "", fmt.Errorf("resolve PVC for pod %s: cluster has no healthy PVCs", podName)
	}

	return "", fmt.Errorf("resolve PVC for pod %s: no healthy PVC matched pod name", podName)
}

func clusterCacheKey(namespace, name string) string {
	return strings.TrimSpace(namespace) + "/" + strings.TrimSpace(name)
}

func instanceCacheKey(namespace, clusterName, podName string) string {
	return clusterCacheKey(namespace, clusterName) + "/" + strings.TrimSpace(podName)
}

func joinScrapeErrors(errs []string) string {
	filtered := make([]string, 0, len(errs))
	for _, err := range errs {
		err = strings.TrimSpace(err)
		if err == "" {
			continue
		}
		filtered = append(filtered, err)
	}
	return strings.Join(filtered, "; ")
}

func cloneClusterMetrics(in ClusterMetrics) ClusterMetrics {
	out := in
	out.Instances = append([]InstanceMetrics(nil), in.Instances...)
	return out
}

var _ Reader = (*Scraper)(nil)
