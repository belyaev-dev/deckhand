// Package store provides the in-memory runtime snapshot used by the Deckhand backend.
package store

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
)

// ResourceKind identifies the CloudNativePG resource type carried by a store event.
type ResourceKind string

const (
	ResourceKindCluster         ResourceKind = "cluster"
	ResourceKindBackup          ResourceKind = "backup"
	ResourceKindScheduledBackup ResourceKind = "scheduled_backup"
)

// Action identifies the kind of mutation applied to the store.
type Action string

const (
	ActionUpsert Action = "upsert"
	ActionDelete Action = "delete"
)

// ChangeEvent describes a mutation that was applied to the in-memory store.
type ChangeEvent struct {
	Kind       ResourceKind `json:"kind"`
	Action     Action       `json:"action"`
	Namespace  string       `json:"namespace"`
	Name       string       `json:"name"`
	OccurredAt time.Time    `json:"occurredAt"`
}

// Store holds the current CloudNativePG runtime snapshot in memory only.
type Store struct {
	mu sync.RWMutex

	clusters         map[string]map[string]*cnpgv1.Cluster
	backups          map[string]map[string]*cnpgv1.Backup
	scheduledBackups map[string]map[string]*cnpgv1.ScheduledBackup

	subscribers      map[uint64]chan ChangeEvent
	nextSubscriberID uint64
}

// New creates an empty in-memory store.
func New() *Store {
	return &Store{
		clusters:         make(map[string]map[string]*cnpgv1.Cluster),
		backups:          make(map[string]map[string]*cnpgv1.Backup),
		scheduledBackups: make(map[string]map[string]*cnpgv1.ScheduledBackup),
		subscribers:      make(map[uint64]chan ChangeEvent),
	}
}

// Subscribe registers a non-blocking event stream for store change notifications.
// The returned unsubscribe function is safe to call multiple times.
func (s *Store) Subscribe(buffer int) (<-chan ChangeEvent, func()) {
	if buffer < 1 {
		buffer = 1
	}

	ch := make(chan ChangeEvent, buffer)

	s.mu.Lock()
	id := s.nextSubscriberID
	s.nextSubscriberID++
	s.subscribers[id] = ch
	s.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if subscribed, ok := s.subscribers[id]; ok {
				delete(s.subscribers, id)
				close(subscribed)
			}
		})
	}

	return ch, unsubscribe
}

// UpsertCluster stores a deep-copied Cluster snapshot keyed by namespace/name.
func (s *Store) UpsertCluster(cluster *cnpgv1.Cluster) error {
	if cluster == nil {
		return errors.New("cluster is required")
	}

	copy := cluster.DeepCopy()
	namespace, name, err := validateKey(copy.Namespace, copy.Name)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := ensureClusterNamespace(s.clusters, namespace)
	bucket[name] = copy
	s.emitLocked(ChangeEvent{
		Kind:       ResourceKindCluster,
		Action:     ActionUpsert,
		Namespace:  namespace,
		Name:       name,
		OccurredAt: time.Now().UTC(),
	})

	return nil
}

// DeleteCluster removes a Cluster snapshot if present.
func (s *Store) DeleteCluster(namespace, name string) error {
	namespace, name, err := validateKey(namespace, name)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !deleteFromNamespace(s.clusters, namespace, name) {
		return nil
	}

	s.emitLocked(ChangeEvent{
		Kind:       ResourceKindCluster,
		Action:     ActionDelete,
		Namespace:  namespace,
		Name:       name,
		OccurredAt: time.Now().UTC(),
	})

	return nil
}

// GetCluster returns a deep-copied Cluster snapshot by namespace/name.
func (s *Store) GetCluster(namespace, name string) (*cnpgv1.Cluster, bool) {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" {
		return nil, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	bucket, ok := s.clusters[namespace]
	if !ok {
		return nil, false
	}
	cluster, ok := bucket[name]
	if !ok {
		return nil, false
	}

	return cluster.DeepCopy(), true
}

// ListClusters returns deep-copied Cluster snapshots, optionally filtered to a namespace.
func (s *Store) ListClusters(namespace string) []*cnpgv1.Cluster {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]*cnpgv1.Cluster, 0)
	for _, cluster := range listClustersLocked(s.clusters, namespace) {
		items = append(items, cluster.DeepCopy())
	}
	return items
}

// UpsertBackup stores a deep-copied Backup snapshot keyed by namespace/name.
func (s *Store) UpsertBackup(backup *cnpgv1.Backup) error {
	if backup == nil {
		return errors.New("backup is required")
	}

	copy := backup.DeepCopy()
	namespace, name, err := validateKey(copy.Namespace, copy.Name)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := ensureBackupNamespace(s.backups, namespace)
	bucket[name] = copy
	s.emitLocked(ChangeEvent{
		Kind:       ResourceKindBackup,
		Action:     ActionUpsert,
		Namespace:  namespace,
		Name:       name,
		OccurredAt: time.Now().UTC(),
	})

	return nil
}

// DeleteBackup removes a Backup snapshot if present.
func (s *Store) DeleteBackup(namespace, name string) error {
	namespace, name, err := validateKey(namespace, name)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !deleteFromNamespace(s.backups, namespace, name) {
		return nil
	}

	s.emitLocked(ChangeEvent{
		Kind:       ResourceKindBackup,
		Action:     ActionDelete,
		Namespace:  namespace,
		Name:       name,
		OccurredAt: time.Now().UTC(),
	})

	return nil
}

// GetBackup returns a deep-copied Backup snapshot by namespace/name.
func (s *Store) GetBackup(namespace, name string) (*cnpgv1.Backup, bool) {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" {
		return nil, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	bucket, ok := s.backups[namespace]
	if !ok {
		return nil, false
	}
	backup, ok := bucket[name]
	if !ok {
		return nil, false
	}

	return backup.DeepCopy(), true
}

// ListBackups returns deep-copied Backup snapshots, optionally filtered to a namespace.
func (s *Store) ListBackups(namespace string) []*cnpgv1.Backup {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]*cnpgv1.Backup, 0)
	for _, backup := range listBackupsLocked(s.backups, namespace) {
		items = append(items, backup.DeepCopy())
	}
	return items
}

// ListBackupsForCluster returns deep-copied Backups associated with a specific Cluster.
func (s *Store) ListBackupsForCluster(namespace, clusterName string) []*cnpgv1.Backup {
	if strings.TrimSpace(clusterName) == "" {
		return []*cnpgv1.Backup{}
	}

	backups := s.ListBackups(namespace)
	filtered := make([]*cnpgv1.Backup, 0, len(backups))
	for _, backup := range backups {
		if backup.Spec.Cluster.Name == clusterName {
			filtered = append(filtered, backup)
		}
	}
	return filtered
}

// UpsertScheduledBackup stores a deep-copied ScheduledBackup snapshot keyed by namespace/name.
func (s *Store) UpsertScheduledBackup(scheduledBackup *cnpgv1.ScheduledBackup) error {
	if scheduledBackup == nil {
		return errors.New("scheduled backup is required")
	}

	copy := scheduledBackup.DeepCopy()
	namespace, name, err := validateKey(copy.Namespace, copy.Name)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := ensureScheduledBackupNamespace(s.scheduledBackups, namespace)
	bucket[name] = copy
	s.emitLocked(ChangeEvent{
		Kind:       ResourceKindScheduledBackup,
		Action:     ActionUpsert,
		Namespace:  namespace,
		Name:       name,
		OccurredAt: time.Now().UTC(),
	})

	return nil
}

// DeleteScheduledBackup removes a ScheduledBackup snapshot if present.
func (s *Store) DeleteScheduledBackup(namespace, name string) error {
	namespace, name, err := validateKey(namespace, name)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !deleteFromNamespace(s.scheduledBackups, namespace, name) {
		return nil
	}

	s.emitLocked(ChangeEvent{
		Kind:       ResourceKindScheduledBackup,
		Action:     ActionDelete,
		Namespace:  namespace,
		Name:       name,
		OccurredAt: time.Now().UTC(),
	})

	return nil
}

// GetScheduledBackup returns a deep-copied ScheduledBackup snapshot by namespace/name.
func (s *Store) GetScheduledBackup(namespace, name string) (*cnpgv1.ScheduledBackup, bool) {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" {
		return nil, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	bucket, ok := s.scheduledBackups[namespace]
	if !ok {
		return nil, false
	}
	scheduledBackup, ok := bucket[name]
	if !ok {
		return nil, false
	}

	return scheduledBackup.DeepCopy(), true
}

// ListScheduledBackups returns deep-copied ScheduledBackup snapshots, optionally filtered to a namespace.
func (s *Store) ListScheduledBackups(namespace string) []*cnpgv1.ScheduledBackup {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]*cnpgv1.ScheduledBackup, 0)
	for _, scheduledBackup := range listScheduledBackupsLocked(s.scheduledBackups, namespace) {
		items = append(items, scheduledBackup.DeepCopy())
	}
	return items
}

// ListScheduledBackupsForCluster returns deep-copied ScheduledBackups associated with a specific Cluster.
func (s *Store) ListScheduledBackupsForCluster(namespace, clusterName string) []*cnpgv1.ScheduledBackup {
	if strings.TrimSpace(clusterName) == "" {
		return []*cnpgv1.ScheduledBackup{}
	}

	scheduledBackups := s.ListScheduledBackups(namespace)
	filtered := make([]*cnpgv1.ScheduledBackup, 0, len(scheduledBackups))
	for _, scheduledBackup := range scheduledBackups {
		if scheduledBackup.Spec.Cluster.Name == clusterName {
			filtered = append(filtered, scheduledBackup)
		}
	}
	return filtered
}

func (s *Store) emitLocked(event ChangeEvent) {
	for _, subscriber := range s.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

func validateKey(namespace, name string) (string, string, error) {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" {
		return "", "", errors.New("namespace is required")
	}
	if name == "" {
		return "", "", errors.New("name is required")
	}
	return namespace, name, nil
}

func ensureClusterNamespace(items map[string]map[string]*cnpgv1.Cluster, namespace string) map[string]*cnpgv1.Cluster {
	if bucket, ok := items[namespace]; ok {
		return bucket
	}
	bucket := make(map[string]*cnpgv1.Cluster)
	items[namespace] = bucket
	return bucket
}

func ensureBackupNamespace(items map[string]map[string]*cnpgv1.Backup, namespace string) map[string]*cnpgv1.Backup {
	if bucket, ok := items[namespace]; ok {
		return bucket
	}
	bucket := make(map[string]*cnpgv1.Backup)
	items[namespace] = bucket
	return bucket
}

func ensureScheduledBackupNamespace(items map[string]map[string]*cnpgv1.ScheduledBackup, namespace string) map[string]*cnpgv1.ScheduledBackup {
	if bucket, ok := items[namespace]; ok {
		return bucket
	}
	bucket := make(map[string]*cnpgv1.ScheduledBackup)
	items[namespace] = bucket
	return bucket
}

func deleteFromNamespace[T any](items map[string]map[string]T, namespace, name string) bool {
	bucket, ok := items[namespace]
	if !ok {
		return false
	}
	if _, ok := bucket[name]; !ok {
		return false
	}
	delete(bucket, name)
	if len(bucket) == 0 {
		delete(items, namespace)
	}
	return true
}

func listClustersLocked(items map[string]map[string]*cnpgv1.Cluster, namespace string) []*cnpgv1.Cluster {
	if namespace = strings.TrimSpace(namespace); namespace != "" {
		clusters := cloneSortedClusters(items[namespace])
		return clusters
	}

	clusters := make([]*cnpgv1.Cluster, 0)
	for _, bucket := range items {
		for _, cluster := range bucket {
			clusters = append(clusters, cluster)
		}
	}
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].Namespace != clusters[j].Namespace {
			return clusters[i].Namespace < clusters[j].Namespace
		}
		return clusters[i].Name < clusters[j].Name
	})
	return clusters
}

func listBackupsLocked(items map[string]map[string]*cnpgv1.Backup, namespace string) []*cnpgv1.Backup {
	if namespace = strings.TrimSpace(namespace); namespace != "" {
		backups := cloneSortedBackups(items[namespace])
		return backups
	}

	backups := make([]*cnpgv1.Backup, 0)
	for _, bucket := range items {
		for _, backup := range bucket {
			backups = append(backups, backup)
		}
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].Namespace != backups[j].Namespace {
			return backups[i].Namespace < backups[j].Namespace
		}
		return backups[i].Name < backups[j].Name
	})
	return backups
}

func listScheduledBackupsLocked(items map[string]map[string]*cnpgv1.ScheduledBackup, namespace string) []*cnpgv1.ScheduledBackup {
	if namespace = strings.TrimSpace(namespace); namespace != "" {
		scheduledBackups := cloneSortedScheduledBackups(items[namespace])
		return scheduledBackups
	}

	scheduledBackups := make([]*cnpgv1.ScheduledBackup, 0)
	for _, bucket := range items {
		for _, scheduledBackup := range bucket {
			scheduledBackups = append(scheduledBackups, scheduledBackup)
		}
	}
	sort.Slice(scheduledBackups, func(i, j int) bool {
		if scheduledBackups[i].Namespace != scheduledBackups[j].Namespace {
			return scheduledBackups[i].Namespace < scheduledBackups[j].Namespace
		}
		return scheduledBackups[i].Name < scheduledBackups[j].Name
	})
	return scheduledBackups
}

func cloneSortedClusters(items map[string]*cnpgv1.Cluster) []*cnpgv1.Cluster {
	clusters := make([]*cnpgv1.Cluster, 0, len(items))
	for _, cluster := range items {
		clusters = append(clusters, cluster)
	}
	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].Name < clusters[j].Name
	})
	return clusters
}

func cloneSortedBackups(items map[string]*cnpgv1.Backup) []*cnpgv1.Backup {
	backups := make([]*cnpgv1.Backup, 0, len(items))
	for _, backup := range items {
		backups = append(backups, backup)
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Name < backups[j].Name
	})
	return backups
}

func cloneSortedScheduledBackups(items map[string]*cnpgv1.ScheduledBackup) []*cnpgv1.ScheduledBackup {
	scheduledBackups := make([]*cnpgv1.ScheduledBackup, 0, len(items))
	for _, scheduledBackup := range items {
		scheduledBackups = append(scheduledBackups, scheduledBackup)
	}
	sort.Slice(scheduledBackups, func(i, j int) bool {
		return scheduledBackups[i].Name < scheduledBackups[j].Name
	})
	return scheduledBackups
}
