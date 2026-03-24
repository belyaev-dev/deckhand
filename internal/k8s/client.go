// Package k8s provides Kubernetes client setup, scheme registration, and
// runtime configuration for Deckhand.
package k8s

import (
	"errors"
	"fmt"
	"strings"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// RuntimeConfig holds the parsed configuration for the Deckhand backend.
type RuntimeConfig struct {
	// ListenAddr is the HTTP server bind address (e.g. ":8080").
	ListenAddr string

	// Kubeconfig is the path to a kubeconfig file. Empty means in-cluster.
	Kubeconfig string

	// Namespaces is the list of namespaces to watch. Empty means all namespaces.
	Namespaces []string
}

// ClientBootstrap captures the Kubernetes runtime primitives Deckhand needs to
// create a typed controller-runtime client and cache.
type ClientBootstrap struct {
	Config       RuntimeConfig
	RESTConfig   *rest.Config
	Scheme       *runtime.Scheme
	CacheOptions ctrlcache.Options
}

// AllNamespaces returns true when the watcher should watch all namespaces.
func (c RuntimeConfig) AllNamespaces() bool {
	return len(c.Namespaces) == 0
}

// ScopeDescription returns a human-friendly description of the configured
// namespace scope for logging and diagnostics.
func (c RuntimeConfig) ScopeDescription() string {
	if c.AllNamespaces() {
		return "all namespaces"
	}
	return strings.Join(c.Namespaces, ",")
}

// Normalize trims values and validates the runtime configuration.
func (c RuntimeConfig) Normalize() (RuntimeConfig, error) {
	normalized := RuntimeConfig{
		ListenAddr: strings.TrimSpace(c.ListenAddr),
		Kubeconfig: strings.TrimSpace(c.Kubeconfig),
	}

	namespaces, err := NormalizeNamespaces(c.Namespaces)
	if err != nil {
		return RuntimeConfig{}, err
	}
	normalized.Namespaces = namespaces

	if normalized.ListenAddr == "" {
		return RuntimeConfig{}, errors.New("listen address is required")
	}

	return normalized, nil
}

// ParseNamespaces splits a comma-separated namespace string into a trimmed
// slice, filtering out empty entries.
func ParseNamespaces(raw string) []string {
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}

	return out
}

// NormalizeNamespaces validates namespace names, strips whitespace, and
// rejects duplicates. An empty result means all namespaces.
func NormalizeNamespaces(namespaces []string) ([]string, error) {
	if len(namespaces) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{}, len(namespaces))
	normalized := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		namespace = strings.TrimSpace(namespace)
		if namespace == "" {
			return nil, errors.New("namespaces must not contain empty values")
		}
		if _, exists := seen[namespace]; exists {
			return nil, fmt.Errorf("duplicate namespace %q", namespace)
		}
		seen[namespace] = struct{}{}
		normalized = append(normalized, namespace)
	}

	return normalized, nil
}

// NewScheme returns a runtime.Scheme with core Kubernetes types and
// CloudNativePG CRD types registered.
func NewScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()

	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering core k8s types: %w", err)
	}

	if err := cnpgv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering cnpg types: %w", err)
	}

	return scheme, nil
}

// BuildRESTConfig returns a *rest.Config from the given RuntimeConfig.
// If Kubeconfig is set, it uses that file; otherwise it falls back to the
// in-cluster configuration.
func BuildRESTConfig(cfg RuntimeConfig) (*rest.Config, error) {
	normalized, err := cfg.Normalize()
	if err != nil {
		return nil, fmt.Errorf("validating runtime config: %w", err)
	}

	if normalized.Kubeconfig != "" {
		restConfig, err := clientcmd.BuildConfigFromFlags("", normalized.Kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("building config from kubeconfig %q: %w", normalized.Kubeconfig, err)
		}
		restConfig = rest.CopyConfig(restConfig)
		rest.AddUserAgent(restConfig, "deckhand")
		return restConfig, nil
	}

	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("building in-cluster config: %w", err)
	}
	restConfig = rest.CopyConfig(restConfig)
	rest.AddUserAgent(restConfig, "deckhand")
	return restConfig, nil
}

// BuildCacheOptions returns controller-runtime cache options for the configured
// namespace scope.
func BuildCacheOptions(cfg RuntimeConfig) (ctrlcache.Options, error) {
	normalized, err := cfg.Normalize()
	if err != nil {
		return ctrlcache.Options{}, fmt.Errorf("validating runtime config: %w", err)
	}

	if normalized.AllNamespaces() {
		return ctrlcache.Options{}, nil
	}

	defaultNamespaces := make(map[string]ctrlcache.Config, len(normalized.Namespaces))
	for _, namespace := range normalized.Namespaces {
		defaultNamespaces[namespace] = ctrlcache.Config{}
	}

	return ctrlcache.Options{DefaultNamespaces: defaultNamespaces}, nil
}

// Bootstrap prepares the primitives needed to construct controller-runtime
// clients and caches.
func Bootstrap(cfg RuntimeConfig) (*ClientBootstrap, error) {
	normalized, err := cfg.Normalize()
	if err != nil {
		return nil, fmt.Errorf("validating runtime config: %w", err)
	}

	scheme, err := NewScheme()
	if err != nil {
		return nil, fmt.Errorf("building scheme: %w", err)
	}

	restConfig, err := BuildRESTConfig(normalized)
	if err != nil {
		return nil, fmt.Errorf("building REST config: %w", err)
	}

	cacheOptions, err := BuildCacheOptions(normalized)
	if err != nil {
		return nil, fmt.Errorf("building cache options: %w", err)
	}

	return &ClientBootstrap{
		Config:       normalized,
		RESTConfig:   restConfig,
		Scheme:       scheme,
		CacheOptions: cacheOptions,
	}, nil
}

// NewCache creates a controller-runtime cache using the prepared bootstrap
// primitives and namespace scope.
func NewCache(bootstrap *ClientBootstrap) (ctrlcache.Cache, error) {
	if bootstrap == nil {
		return nil, errors.New("client bootstrap is required")
	}
	if bootstrap.RESTConfig == nil {
		return nil, errors.New("rest config is required")
	}
	if bootstrap.Scheme == nil {
		return nil, errors.New("scheme is required")
	}

	cacheOptions := bootstrap.CacheOptions
	cacheOptions.Scheme = bootstrap.Scheme

	cache, err := ctrlcache.New(bootstrap.RESTConfig, cacheOptions)
	if err != nil {
		return nil, fmt.Errorf("creating controller-runtime cache: %w", err)
	}

	return cache, nil
}

// NewClient creates a typed controller-runtime client using the prepared
// bootstrap primitives.
func NewClient(bootstrap *ClientBootstrap) (ctrlclient.Client, error) {
	if bootstrap == nil {
		return nil, errors.New("client bootstrap is required")
	}
	if bootstrap.RESTConfig == nil {
		return nil, errors.New("rest config is required")
	}
	if bootstrap.Scheme == nil {
		return nil, errors.New("scheme is required")
	}

	client, err := ctrlclient.New(bootstrap.RESTConfig, ctrlclient.Options{Scheme: bootstrap.Scheme})
	if err != nil {
		return nil, fmt.Errorf("creating controller-runtime client: %w", err)
	}

	return client, nil
}
