package k8s

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestWatcherSyncsCNPGResources(t *testing.T) {
	st := store.New()
	events, unsubscribe := st.Subscribe(32)
	defer unsubscribe()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	cache := newFakeWatcherCache(nil)
	cluster := watcherTestCluster("team-a", "alpha")
	backup := watcherTestBackup("team-a", "alpha-backup", "alpha")
	scheduledBackup := watcherTestScheduledBackup("team-a", "alpha-nightly", "alpha")
	cache.seed(cluster, backup, scheduledBackup)

	watcher := newTestWatcher(t, RuntimeConfig{ListenAddr: ":8080"}, cache, st, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- watcher.Start(ctx)
	}()

	waitForReady(t, watcher.Ready())

	assertClusterPhase(t, st, "team-a", "alpha", "healthy")
	assertBackupPresent(t, st, "team-a", "alpha-backup")
	assertScheduledBackupPresent(t, st, "team-a", "alpha-nightly")

	updatedCluster := watcherTestCluster("team-a", "alpha")
	updatedCluster.Status.Phase = "updating"
	cache.update(cluster, updatedCluster)
	assertClusterPhase(t, st, "team-a", "alpha", "updating")

	newBackup := watcherTestBackup("team-a", "alpha-backup-2", "alpha")
	cache.add(newBackup)
	assertBackupPresent(t, st, "team-a", "alpha-backup-2")

	cache.delete(scheduledBackup)
	assertScheduledBackupMissing(t, st, "team-a", "alpha-nightly")

	assertEventSet(t, events, []struct {
		kind      store.ResourceKind
		action    store.Action
		namespace string
		name      string
	}{
		{kind: store.ResourceKindCluster, action: store.ActionUpsert, namespace: "team-a", name: "alpha"},
		{kind: store.ResourceKindBackup, action: store.ActionUpsert, namespace: "team-a", name: "alpha-backup"},
		{kind: store.ResourceKindScheduledBackup, action: store.ActionUpsert, namespace: "team-a", name: "alpha-nightly"},
	})
	assertEventSequence(t, events, []struct {
		kind      store.ResourceKind
		action    store.Action
		namespace string
		name      string
	}{
		{kind: store.ResourceKindCluster, action: store.ActionUpsert, namespace: "team-a", name: "alpha"},
		{kind: store.ResourceKindBackup, action: store.ActionUpsert, namespace: "team-a", name: "alpha-backup-2"},
		{kind: store.ResourceKindScheduledBackup, action: store.ActionDelete, namespace: "team-a", name: "alpha-nightly"},
	})

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("watcher.Start() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for watcher to stop")
	}

	for _, fragment := range []string{
		"starting cnpg watcher",
		"cnpg watcher synced",
		"applied cnpg resource event",
		`"resource_kind":"cluster"`,
		`"resource_kind":"backup"`,
		`"resource_kind":"scheduled_backup"`,
		`"action":"delete"`,
		`"namespace":"team-a"`,
		`"name":"alpha-nightly"`,
	} {
		if !strings.Contains(logs.String(), fragment) {
			t.Fatalf("expected logs to contain %q, got %s", fragment, logs.String())
		}
	}
}

func TestWatcherHonorsNamespaceScope(t *testing.T) {
	st := store.New()
	events, unsubscribe := st.Subscribe(16)
	defer unsubscribe()

	cache := newFakeWatcherCache([]string{"team-a"})
	cache.seed(
		watcherTestCluster("team-a", "alpha"),
		watcherTestCluster("team-b", "bravo"),
		watcherTestBackup("team-a", "alpha-backup", "alpha"),
		watcherTestBackup("team-b", "bravo-backup", "bravo"),
	)

	watcher := newTestWatcher(t, RuntimeConfig{ListenAddr: ":8080", Namespaces: []string{"team-a"}}, cache, st, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- watcher.Start(ctx)
	}()

	waitForReady(t, watcher.Ready())

	assertClusterPhase(t, st, "team-a", "alpha", "healthy")
	assertBackupPresent(t, st, "team-a", "alpha-backup")
	if got := len(st.ListClusters("team-b")); got != 0 {
		t.Fatalf("len(ListClusters(team-b)) = %d, want 0", got)
	}
	if got := len(st.ListBackups("team-b")); got != 0 {
		t.Fatalf("len(ListBackups(team-b)) = %d, want 0", got)
	}

	cache.add(watcherTestScheduledBackup("team-b", "bravo-nightly", "bravo"))
	if got := len(st.ListScheduledBackups("team-b")); got != 0 {
		t.Fatalf("len(ListScheduledBackups(team-b)) after out-of-scope add = %d, want 0", got)
	}

	cache.add(watcherTestScheduledBackup("team-a", "alpha-nightly", "alpha"))
	assertScheduledBackupPresent(t, st, "team-a", "alpha-nightly")

	assertEventSet(t, events, []struct {
		kind      store.ResourceKind
		action    store.Action
		namespace string
		name      string
	}{
		{kind: store.ResourceKindCluster, action: store.ActionUpsert, namespace: "team-a", name: "alpha"},
		{kind: store.ResourceKindBackup, action: store.ActionUpsert, namespace: "team-a", name: "alpha-backup"},
	})
	assertEventSequence(t, events, []struct {
		kind      store.ResourceKind
		action    store.Action
		namespace string
		name      string
	}{
		{kind: store.ResourceKindScheduledBackup, action: store.ActionUpsert, namespace: "team-a", name: "alpha-nightly"},
	})

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("watcher.Start() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for watcher to stop")
	}
}

func newTestWatcher(t *testing.T, cfg RuntimeConfig, cache watcherCache, st *store.Store, logger *slog.Logger) *Watcher {
	t.Helper()

	watcher, err := newWatcherWithCache(cfg, cache, st, logger, 2*time.Second)
	if err != nil {
		t.Fatalf("newWatcherWithCache() error: %v", err)
	}
	return watcher
}

func waitForReady(t *testing.T, ready <-chan struct{}) {
	t.Helper()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for watcher readiness")
	}
}

func assertClusterPhase(t *testing.T, st *store.Store, namespace, name, wantPhase string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cluster, ok := st.GetCluster(namespace, name)
		if ok && cluster.Status.Phase == wantPhase {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	cluster, ok := st.GetCluster(namespace, name)
	if !ok {
		t.Fatalf("GetCluster(%q, %q) found no cluster", namespace, name)
	}
	t.Fatalf("cluster %s/%s phase = %q, want %q", namespace, name, cluster.Status.Phase, wantPhase)
}

func assertBackupPresent(t *testing.T, st *store.Store, namespace, name string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := st.GetBackup(namespace, name); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("GetBackup(%q, %q) found no backup", namespace, name)
}

func assertScheduledBackupPresent(t *testing.T, st *store.Store, namespace, name string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := st.GetScheduledBackup(namespace, name); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("GetScheduledBackup(%q, %q) found no scheduled backup", namespace, name)
}

func assertScheduledBackupMissing(t *testing.T, st *store.Store, namespace, name string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := st.GetScheduledBackup(namespace, name); !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("GetScheduledBackup(%q, %q) still found the scheduled backup", namespace, name)
}

func assertEventSequence(t *testing.T, events <-chan store.ChangeEvent, want []struct {
	kind      store.ResourceKind
	action    store.Action
	namespace string
	name      string
}) {
	t.Helper()

	for i, expected := range want {
		select {
		case event := <-events:
			if event.Kind != expected.kind || event.Action != expected.action || event.Namespace != expected.namespace || event.Name != expected.name {
				t.Fatalf("event %d = %#v, want kind=%q action=%q namespace=%q name=%q", i, event, expected.kind, expected.action, expected.namespace, expected.name)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
}

func assertEventSet(t *testing.T, events <-chan store.ChangeEvent, want []struct {
	kind      store.ResourceKind
	action    store.Action
	namespace string
	name      string
}) {
	t.Helper()

	remaining := make(map[string]struct{}, len(want))
	for _, expected := range want {
		remaining[eventKey(expected.kind, expected.action, expected.namespace, expected.name)] = struct{}{}
	}

	for i := 0; i < len(want); i++ {
		select {
		case event := <-events:
			key := eventKey(event.Kind, event.Action, event.Namespace, event.Name)
			if _, ok := remaining[key]; !ok {
				t.Fatalf("unexpected event %#v while waiting for unordered startup set", event)
			}
			delete(remaining, key)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for unordered event %d", i)
		}
	}

	if len(remaining) != 0 {
		t.Fatalf("missing expected events: %v", remaining)
	}
}

func eventKey(kind store.ResourceKind, action store.Action, namespace, name string) string {
	return strings.Join([]string{string(kind), string(action), namespace, name}, "|")
}

type fakeWatcherCache struct {
	mu                sync.Mutex
	allowedNamespaces map[string]struct{}
	initialObjects    map[reflect.Type][]ctrlclient.Object
	informers         map[reflect.Type]*fakeInformer
	started           bool
	synced            bool
	startErr          error
	syncCh            chan struct{}
}

func newFakeWatcherCache(namespaces []string) *fakeWatcherCache {
	allowed := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		allowed[namespace] = struct{}{}
	}
	if len(allowed) == 0 {
		allowed = nil
	}

	return &fakeWatcherCache{
		allowedNamespaces: allowed,
		initialObjects:    make(map[reflect.Type][]ctrlclient.Object),
		informers:         make(map[reflect.Type]*fakeInformer),
		syncCh:            make(chan struct{}),
	}
}

func (c *fakeWatcherCache) seed(objects ...ctrlclient.Object) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, object := range objects {
		if !c.inScope(object.GetNamespace()) {
			continue
		}
		key := reflect.TypeOf(object)
		c.initialObjects[key] = append(c.initialObjects[key], object.DeepCopyObject().(ctrlclient.Object))
	}
}

func (c *fakeWatcherCache) GetInformer(_ context.Context, obj ctrlclient.Object, _ ...ctrlcache.InformerGetOption) (ctrlcache.Informer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := reflect.TypeOf(obj)
	if informer, ok := c.informers[key]; ok {
		return informer, nil
	}

	informer := &fakeInformer{}
	c.informers[key] = informer
	return informer, nil
}

func (c *fakeWatcherCache) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		<-ctx.Done()
		return nil
	}
	c.started = true
	if c.startErr != nil {
		err := c.startErr
		c.mu.Unlock()
		return err
	}

	initialByType := make(map[reflect.Type][]ctrlclient.Object, len(c.initialObjects))
	for key, objects := range c.initialObjects {
		copied := make([]ctrlclient.Object, 0, len(objects))
		for _, object := range objects {
			copied = append(copied, object.DeepCopyObject().(ctrlclient.Object))
		}
		initialByType[key] = copied
	}
	informers := make(map[reflect.Type]*fakeInformer, len(c.informers))
	for key, informer := range c.informers {
		informers[key] = informer
	}
	c.mu.Unlock()

	for key, objects := range initialByType {
		informer, ok := informers[key]
		if !ok {
			continue
		}
		for _, object := range objects {
			informer.add(object)
		}
	}

	c.mu.Lock()
	if !c.synced {
		c.synced = true
		close(c.syncCh)
	}
	c.mu.Unlock()

	<-ctx.Done()
	return nil
}

func (c *fakeWatcherCache) WaitForCacheSync(ctx context.Context) bool {
	c.mu.Lock()
	if c.synced {
		c.mu.Unlock()
		return true
	}
	syncCh := c.syncCh
	c.mu.Unlock()

	select {
	case <-syncCh:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *fakeWatcherCache) add(obj ctrlclient.Object) {
	if !c.inScope(obj.GetNamespace()) {
		return
	}
	informer := c.getInformerForType(reflect.TypeOf(obj))
	informer.add(obj.DeepCopyObject().(ctrlclient.Object))
}

func (c *fakeWatcherCache) update(oldObj, newObj ctrlclient.Object) {
	if !c.inScope(newObj.GetNamespace()) {
		return
	}
	informer := c.getInformerForType(reflect.TypeOf(newObj))
	informer.update(oldObj.DeepCopyObject().(ctrlclient.Object), newObj.DeepCopyObject().(ctrlclient.Object))
}

func (c *fakeWatcherCache) delete(obj ctrlclient.Object) {
	if !c.inScope(obj.GetNamespace()) {
		return
	}
	informer := c.getInformerForType(reflect.TypeOf(obj))
	informer.delete(obj.DeepCopyObject().(ctrlclient.Object))
}

func (c *fakeWatcherCache) getInformerForType(key reflect.Type) *fakeInformer {
	c.mu.Lock()
	defer c.mu.Unlock()

	informer, ok := c.informers[key]
	if !ok {
		informer = &fakeInformer{}
		c.informers[key] = informer
	}
	return informer
}

func (c *fakeWatcherCache) inScope(namespace string) bool {
	if len(c.allowedNamespaces) == 0 {
		return true
	}
	_, ok := c.allowedNamespaces[namespace]
	return ok
}

type fakeInformer struct {
	mu       sync.Mutex
	handlers []toolscache.ResourceEventHandler
}

func (i *fakeInformer) AddEventHandler(handler toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.handlers = append(i.handlers, handler)
	return nil, nil
}

func (i *fakeInformer) AddEventHandlerWithResyncPeriod(handler toolscache.ResourceEventHandler, _ time.Duration) (toolscache.ResourceEventHandlerRegistration, error) {
	return i.AddEventHandler(handler)
}

func (i *fakeInformer) RemoveEventHandler(toolscache.ResourceEventHandlerRegistration) error {
	return nil
}

func (i *fakeInformer) AddIndexers(toolscache.Indexers) error {
	return nil
}

func (i *fakeInformer) HasSynced() bool {
	return true
}

func (i *fakeInformer) IsStopped() bool {
	return false
}

func (i *fakeInformer) add(obj ctrlclient.Object) {
	i.mu.Lock()
	handlers := append([]toolscache.ResourceEventHandler(nil), i.handlers...)
	i.mu.Unlock()

	for _, handler := range handlers {
		handler.OnAdd(obj, false)
	}
}

func (i *fakeInformer) update(oldObj, newObj ctrlclient.Object) {
	i.mu.Lock()
	handlers := append([]toolscache.ResourceEventHandler(nil), i.handlers...)
	i.mu.Unlock()

	for _, handler := range handlers {
		handler.OnUpdate(oldObj, newObj)
	}
}

func (i *fakeInformer) delete(obj ctrlclient.Object) {
	i.mu.Lock()
	handlers := append([]toolscache.ResourceEventHandler(nil), i.handlers...)
	i.mu.Unlock()

	for _, handler := range handlers {
		handler.OnDelete(obj)
	}
}

func watcherTestCluster(namespace, name string) *cnpgv1.Cluster {
	createdAt := metav1.NewTime(time.Date(2026, time.March, 24, 12, 0, 0, 0, time.UTC))
	return &cnpgv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: createdAt,
		},
		Spec: cnpgv1.ClusterSpec{
			Instances: 3,
			ImageName: "ghcr.io/cloudnative-pg/postgresql:16",
		},
		Status: cnpgv1.ClusterStatus{
			Phase:          "healthy",
			ReadyInstances: 3,
			CurrentPrimary: name + "-1",
		},
	}
}

func watcherTestBackup(namespace, name, clusterName string) *cnpgv1.Backup {
	createdAt := metav1.NewTime(time.Date(2026, time.March, 24, 11, 0, 0, 0, time.UTC))
	stoppedAt := metav1.NewTime(time.Date(2026, time.March, 24, 11, 5, 0, 0, time.UTC))
	return &cnpgv1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: createdAt,
		},
		Spec: cnpgv1.BackupSpec{
			Cluster: cnpgv1.LocalObjectReference{Name: clusterName},
			Method:  cnpgv1.BackupMethod("barmanObjectStore"),
		},
		Status: cnpgv1.BackupStatus{
			Phase:     cnpgv1.BackupPhase("completed"),
			Method:    cnpgv1.BackupMethod("barmanObjectStore"),
			StartedAt: &createdAt,
			StoppedAt: &stoppedAt,
		},
	}
}

func watcherTestScheduledBackup(namespace, name, clusterName string) *cnpgv1.ScheduledBackup {
	createdAt := metav1.NewTime(time.Date(2026, time.March, 24, 10, 0, 0, 0, time.UTC))
	lastSchedule := metav1.NewTime(time.Date(2026, time.March, 24, 10, 0, 0, 0, time.UTC))
	nextSchedule := metav1.NewTime(time.Date(2026, time.March, 25, 10, 0, 0, 0, time.UTC))
	immediate := true
	return &cnpgv1.ScheduledBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: createdAt,
		},
		Spec: cnpgv1.ScheduledBackupSpec{
			Cluster:   cnpgv1.LocalObjectReference{Name: clusterName},
			Schedule:  "0 0 0 * * *",
			Method:    cnpgv1.BackupMethod("barmanObjectStore"),
			Immediate: &immediate,
		},
		Status: cnpgv1.ScheduledBackupStatus{
			LastScheduleTime: &lastSchedule,
			NextScheduleTime: &nextSchedule,
		},
	}
}

var _ watcherCache = (*fakeWatcherCache)(nil)
var _ ctrlcache.Informer = (*fakeInformer)(nil)
var _ toolscache.ResourceEventHandler = toolscache.ResourceEventHandlerFuncs{}
var _ = schema.GroupVersionKind{}
