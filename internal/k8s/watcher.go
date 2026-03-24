// Package k8s provides Kubernetes client setup, scheme registration, and
// runtime configuration for Deckhand.
package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	toolscache "k8s.io/client-go/tools/cache"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultWatcherSyncTimeout = 30 * time.Second
	watcherSyncPollInterval   = 25 * time.Millisecond
)

type watcherCache interface {
	GetInformer(ctx context.Context, obj ctrlclient.Object, opts ...ctrlcache.InformerGetOption) (ctrlcache.Informer, error)
	Start(ctx context.Context) error
	WaitForCacheSync(ctx context.Context) bool
}

// Watcher keeps the in-memory store synchronized with CloudNativePG resources
// observed through a controller-runtime cache.
type Watcher struct {
	config      RuntimeConfig
	cache       watcherCache
	store       *store.Store
	logger      *slog.Logger
	syncTimeout time.Duration

	mu        sync.Mutex
	started   bool
	readyCh   chan struct{}
	readyOnce sync.Once
}

// NewWatcher builds a CloudNativePG watcher backed by a real controller-runtime
// cache created from the provided bootstrap configuration.
func NewWatcher(bootstrap *ClientBootstrap, st *store.Store, logger *slog.Logger) (*Watcher, error) {
	if bootstrap == nil {
		return nil, errors.New("client bootstrap is required")
	}

	cache, err := NewCache(bootstrap)
	if err != nil {
		return nil, err
	}

	return newWatcherWithCache(bootstrap.Config, cache, st, logger, defaultWatcherSyncTimeout)
}

// Ready returns a channel that closes once the watcher has registered its
// handlers, started the cache, and observed a successful initial sync.
func (w *Watcher) Ready() <-chan struct{} {
	return w.readyCh
}

// Start runs the watcher until the context is canceled or the backing cache
// returns a fatal error.
func (w *Watcher) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}

	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return errors.New("watcher already started")
	}
	w.started = true
	w.mu.Unlock()

	if err := w.registerHandlers(ctx); err != nil {
		w.logger.Error("failed to register cnpg watcher handlers", "error", err)
		return err
	}

	w.logger.Info("starting cnpg watcher",
		"namespace_scope", w.config.ScopeDescription(),
		"all_namespaces", w.config.AllNamespaces(),
		"sync_timeout_ms", w.syncTimeout.Milliseconds(),
	)

	errCh := make(chan error, 1)
	go func() {
		errCh <- w.cache.Start(ctx)
	}()

	if err := w.waitForInitialSync(ctx, errCh); err != nil {
		w.logger.Error("cnpg watcher cache sync failed",
			"error", err,
			"namespace_scope", w.config.ScopeDescription(),
			"all_namespaces", w.config.AllNamespaces(),
		)
		return err
	}

	w.readyOnce.Do(func() {
		close(w.readyCh)
	})
	w.logger.Info("cnpg watcher synced",
		"namespace_scope", w.config.ScopeDescription(),
		"all_namespaces", w.config.AllNamespaces(),
	)

	err := <-errCh
	if err != nil {
		w.logger.Error("cnpg watcher stopped with error", "error", err)
		return fmt.Errorf("run watcher cache: %w", err)
	}

	w.logger.Info("cnpg watcher stopped")
	return nil
}

func newWatcherWithCache(cfg RuntimeConfig, cache watcherCache, st *store.Store, logger *slog.Logger, syncTimeout time.Duration) (*Watcher, error) {
	if cache == nil {
		return nil, errors.New("watcher cache is required")
	}
	if st == nil {
		return nil, errors.New("store is required")
	}

	normalized, err := cfg.Normalize()
	if err != nil {
		return nil, fmt.Errorf("validating watcher runtime config: %w", err)
	}
	if syncTimeout <= 0 {
		syncTimeout = defaultWatcherSyncTimeout
	}

	return &Watcher{
		config:      normalized,
		cache:       cache,
		store:       st,
		logger:      ensureWatcherLogger(logger),
		syncTimeout: syncTimeout,
		readyCh:     make(chan struct{}),
	}, nil
}

func (w *Watcher) waitForInitialSync(ctx context.Context, errCh <-chan error) error {
	deadline := time.NewTimer(w.syncTimeout)
	defer deadline.Stop()

	for {
		syncCtx, cancel := context.WithTimeout(ctx, watcherSyncPollInterval)
		synced := w.cache.WaitForCacheSync(syncCtx)
		cancel()
		if synced {
			return nil
		}

		select {
		case err := <-errCh:
			if err != nil {
				return fmt.Errorf("start watcher cache: %w", err)
			}
			if ctx.Err() != nil {
				return fmt.Errorf("wait for watcher cache sync: %w", ctx.Err())
			}
			return errors.New("wait for watcher cache sync: cache stopped before initial sync")
		case <-deadline.C:
			return fmt.Errorf("wait for watcher cache sync: timed out after %s", w.syncTimeout)
		case <-ctx.Done():
			return fmt.Errorf("wait for watcher cache sync: %w", ctx.Err())
		default:
		}
	}
}

func (w *Watcher) registerHandlers(ctx context.Context) error {
	if err := w.registerClusterHandler(ctx); err != nil {
		return err
	}
	if err := w.registerBackupHandler(ctx); err != nil {
		return err
	}
	if err := w.registerScheduledBackupHandler(ctx); err != nil {
		return err
	}
	return nil
}

func (w *Watcher) registerClusterHandler(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &cnpgv1.Cluster{})
	if err != nil {
		return fmt.Errorf("get cluster informer: %w", err)
	}

	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			cluster, ok := obj.(*cnpgv1.Cluster)
			if !ok {
				w.logUnexpectedObject(store.ResourceKindCluster, obj)
				return
			}
			w.upsertCluster(cluster)
		},
		UpdateFunc: func(_, newObj any) {
			cluster, ok := newObj.(*cnpgv1.Cluster)
			if !ok {
				w.logUnexpectedObject(store.ResourceKindCluster, newObj)
				return
			}
			w.upsertCluster(cluster)
		},
		DeleteFunc: func(obj any) {
			namespace, name, ok := extractNamespacedName(obj)
			if !ok {
				w.logUnexpectedObject(store.ResourceKindCluster, obj)
				return
			}
			w.deleteCluster(namespace, name)
		},
	})
	if err != nil {
		return fmt.Errorf("register cluster event handler: %w", err)
	}

	return nil
}

func (w *Watcher) registerBackupHandler(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &cnpgv1.Backup{})
	if err != nil {
		return fmt.Errorf("get backup informer: %w", err)
	}

	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			backup, ok := obj.(*cnpgv1.Backup)
			if !ok {
				w.logUnexpectedObject(store.ResourceKindBackup, obj)
				return
			}
			w.upsertBackup(backup)
		},
		UpdateFunc: func(_, newObj any) {
			backup, ok := newObj.(*cnpgv1.Backup)
			if !ok {
				w.logUnexpectedObject(store.ResourceKindBackup, newObj)
				return
			}
			w.upsertBackup(backup)
		},
		DeleteFunc: func(obj any) {
			namespace, name, ok := extractNamespacedName(obj)
			if !ok {
				w.logUnexpectedObject(store.ResourceKindBackup, obj)
				return
			}
			w.deleteBackup(namespace, name)
		},
	})
	if err != nil {
		return fmt.Errorf("register backup event handler: %w", err)
	}

	return nil
}

func (w *Watcher) registerScheduledBackupHandler(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &cnpgv1.ScheduledBackup{})
	if err != nil {
		return fmt.Errorf("get scheduled backup informer: %w", err)
	}

	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			scheduledBackup, ok := obj.(*cnpgv1.ScheduledBackup)
			if !ok {
				w.logUnexpectedObject(store.ResourceKindScheduledBackup, obj)
				return
			}
			w.upsertScheduledBackup(scheduledBackup)
		},
		UpdateFunc: func(_, newObj any) {
			scheduledBackup, ok := newObj.(*cnpgv1.ScheduledBackup)
			if !ok {
				w.logUnexpectedObject(store.ResourceKindScheduledBackup, newObj)
				return
			}
			w.upsertScheduledBackup(scheduledBackup)
		},
		DeleteFunc: func(obj any) {
			namespace, name, ok := extractNamespacedName(obj)
			if !ok {
				w.logUnexpectedObject(store.ResourceKindScheduledBackup, obj)
				return
			}
			w.deleteScheduledBackup(namespace, name)
		},
	})
	if err != nil {
		return fmt.Errorf("register scheduled backup event handler: %w", err)
	}

	return nil
}

func (w *Watcher) upsertCluster(cluster *cnpgv1.Cluster) {
	if err := w.store.UpsertCluster(cluster); err != nil {
		w.logger.Error("failed to apply cluster event",
			"error", err,
			"resource_kind", store.ResourceKindCluster,
			"action", store.ActionUpsert,
			"namespace", cluster.GetNamespace(),
			"name", cluster.GetName(),
		)
		return
	}

	w.logger.Info("applied cnpg resource event",
		"resource_kind", store.ResourceKindCluster,
		"action", store.ActionUpsert,
		"namespace", cluster.GetNamespace(),
		"name", cluster.GetName(),
	)
}

func (w *Watcher) deleteCluster(namespace, name string) {
	if err := w.store.DeleteCluster(namespace, name); err != nil {
		w.logger.Error("failed to apply cluster event",
			"error", err,
			"resource_kind", store.ResourceKindCluster,
			"action", store.ActionDelete,
			"namespace", namespace,
			"name", name,
		)
		return
	}

	w.logger.Info("applied cnpg resource event",
		"resource_kind", store.ResourceKindCluster,
		"action", store.ActionDelete,
		"namespace", namespace,
		"name", name,
	)
}

func (w *Watcher) upsertBackup(backup *cnpgv1.Backup) {
	if err := w.store.UpsertBackup(backup); err != nil {
		w.logger.Error("failed to apply backup event",
			"error", err,
			"resource_kind", store.ResourceKindBackup,
			"action", store.ActionUpsert,
			"namespace", backup.GetNamespace(),
			"name", backup.GetName(),
		)
		return
	}

	w.logger.Info("applied cnpg resource event",
		"resource_kind", store.ResourceKindBackup,
		"action", store.ActionUpsert,
		"namespace", backup.GetNamespace(),
		"name", backup.GetName(),
	)
}

func (w *Watcher) deleteBackup(namespace, name string) {
	if err := w.store.DeleteBackup(namespace, name); err != nil {
		w.logger.Error("failed to apply backup event",
			"error", err,
			"resource_kind", store.ResourceKindBackup,
			"action", store.ActionDelete,
			"namespace", namespace,
			"name", name,
		)
		return
	}

	w.logger.Info("applied cnpg resource event",
		"resource_kind", store.ResourceKindBackup,
		"action", store.ActionDelete,
		"namespace", namespace,
		"name", name,
	)
}

func (w *Watcher) upsertScheduledBackup(scheduledBackup *cnpgv1.ScheduledBackup) {
	if err := w.store.UpsertScheduledBackup(scheduledBackup); err != nil {
		w.logger.Error("failed to apply scheduled backup event",
			"error", err,
			"resource_kind", store.ResourceKindScheduledBackup,
			"action", store.ActionUpsert,
			"namespace", scheduledBackup.GetNamespace(),
			"name", scheduledBackup.GetName(),
		)
		return
	}

	w.logger.Info("applied cnpg resource event",
		"resource_kind", store.ResourceKindScheduledBackup,
		"action", store.ActionUpsert,
		"namespace", scheduledBackup.GetNamespace(),
		"name", scheduledBackup.GetName(),
	)
}

func (w *Watcher) deleteScheduledBackup(namespace, name string) {
	if err := w.store.DeleteScheduledBackup(namespace, name); err != nil {
		w.logger.Error("failed to apply scheduled backup event",
			"error", err,
			"resource_kind", store.ResourceKindScheduledBackup,
			"action", store.ActionDelete,
			"namespace", namespace,
			"name", name,
		)
		return
	}

	w.logger.Info("applied cnpg resource event",
		"resource_kind", store.ResourceKindScheduledBackup,
		"action", store.ActionDelete,
		"namespace", namespace,
		"name", name,
	)
}

func (w *Watcher) logUnexpectedObject(kind store.ResourceKind, obj any) {
	w.logger.Error("received unexpected cnpg object type",
		"resource_kind", kind,
		"object_type", fmt.Sprintf("%T", obj),
	)
}

func extractNamespacedName(obj any) (string, string, bool) {
	switch typed := obj.(type) {
	case ctrlclient.Object:
		return typed.GetNamespace(), typed.GetName(), true
	case toolscache.DeletedFinalStateUnknown:
		accessor, ok := typed.Obj.(ctrlclient.Object)
		if !ok {
			return "", "", false
		}
		return accessor.GetNamespace(), accessor.GetName(), true
	default:
		return "", "", false
	}
}

func ensureWatcherLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
