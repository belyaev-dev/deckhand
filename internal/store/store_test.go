package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStore(t *testing.T) {
	t.Run("stores deep-copied snapshots and starts empty after restart", func(t *testing.T) {
		st := New()

		cluster := testCluster("team-a", "alpha")
		backup := testBackup("team-a", "alpha-backup", "alpha")
		scheduledBackup := testScheduledBackup("team-a", "alpha-nightly", "alpha")

		if err := st.UpsertCluster(cluster); err != nil {
			t.Fatalf("UpsertCluster() error: %v", err)
		}
		if err := st.UpsertBackup(backup); err != nil {
			t.Fatalf("UpsertBackup() error: %v", err)
		}
		if err := st.UpsertScheduledBackup(scheduledBackup); err != nil {
			t.Fatalf("UpsertScheduledBackup() error: %v", err)
		}

		cluster.Status.Phase = "mutated-after-store"
		gotCluster, ok := st.GetCluster("team-a", "alpha")
		if !ok {
			t.Fatal("GetCluster() found no cluster")
		}
		if gotCluster.Status.Phase != "healthy" {
			t.Fatalf("stored cluster phase = %q, want %q", gotCluster.Status.Phase, "healthy")
		}

		gotCluster.Status.Phase = "mutated-after-read"
		gotClusterAgain, ok := st.GetCluster("team-a", "alpha")
		if !ok {
			t.Fatal("GetCluster() found no cluster on second read")
		}
		if gotClusterAgain.Status.Phase != "healthy" {
			t.Fatalf("stored cluster phase after read mutation = %q, want %q", gotClusterAgain.Status.Phase, "healthy")
		}

		if got := len(st.ListClusters("")); got != 1 {
			t.Fatalf("len(ListClusters(all)) = %d, want 1", got)
		}
		if got := len(st.ListBackupsForCluster("team-a", "alpha")); got != 1 {
			t.Fatalf("len(ListBackupsForCluster()) = %d, want 1", got)
		}
		if got := len(st.ListScheduledBackupsForCluster("team-a", "alpha")); got != 1 {
			t.Fatalf("len(ListScheduledBackupsForCluster()) = %d, want 1", got)
		}

		if err := st.DeleteScheduledBackup("team-a", "alpha-nightly"); err != nil {
			t.Fatalf("DeleteScheduledBackup() error: %v", err)
		}
		if err := st.DeleteBackup("team-a", "alpha-backup"); err != nil {
			t.Fatalf("DeleteBackup() error: %v", err)
		}
		if err := st.DeleteCluster("team-a", "alpha"); err != nil {
			t.Fatalf("DeleteCluster() error: %v", err)
		}

		if got := len(st.ListClusters("")); got != 0 {
			t.Fatalf("len(ListClusters(all)) after delete = %d, want 0", got)
		}

		st = New()
		if got := len(st.ListClusters("")); got != 0 {
			t.Fatalf("len(ListClusters(all)) after restart = %d, want 0", got)
		}
		if got := len(st.ListBackups("")); got != 0 {
			t.Fatalf("len(ListBackups(all)) after restart = %d, want 0", got)
		}
		if got := len(st.ListScheduledBackups("")); got != 0 {
			t.Fatalf("len(ListScheduledBackups(all)) after restart = %d, want 0", got)
		}
	})

	t.Run("emits change events with resource metadata", func(t *testing.T) {
		st := New()
		events, unsubscribe := st.Subscribe(8)
		defer unsubscribe()

		if err := st.UpsertCluster(testCluster("team-a", "alpha")); err != nil {
			t.Fatalf("UpsertCluster() error: %v", err)
		}
		if err := st.UpsertBackup(testBackup("team-a", "alpha-backup", "alpha")); err != nil {
			t.Fatalf("UpsertBackup() error: %v", err)
		}
		if err := st.UpsertScheduledBackup(testScheduledBackup("team-a", "alpha-nightly", "alpha")); err != nil {
			t.Fatalf("UpsertScheduledBackup() error: %v", err)
		}
		if err := st.DeleteCluster("team-a", "alpha"); err != nil {
			t.Fatalf("DeleteCluster() error: %v", err)
		}

		want := []struct {
			kind      ResourceKind
			action    Action
			namespace string
			name      string
		}{
			{kind: ResourceKindCluster, action: ActionUpsert, namespace: "team-a", name: "alpha"},
			{kind: ResourceKindBackup, action: ActionUpsert, namespace: "team-a", name: "alpha-backup"},
			{kind: ResourceKindScheduledBackup, action: ActionUpsert, namespace: "team-a", name: "alpha-nightly"},
			{kind: ResourceKindCluster, action: ActionDelete, namespace: "team-a", name: "alpha"},
		}

		for i, expected := range want {
			event := nextEvent(t, events)
			if event.Kind != expected.kind || event.Action != expected.action || event.Namespace != expected.namespace || event.Name != expected.name {
				t.Fatalf("event %d = %#v, want kind=%q action=%q namespace=%q name=%q", i, event, expected.kind, expected.action, expected.namespace, expected.name)
			}
			if event.OccurredAt.IsZero() {
				t.Fatalf("event %d has zero OccurredAt", i)
			}
		}
	})

	t.Run("supports concurrent upserts and reads", func(t *testing.T) {
		st := New()

		const workers = 24
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()

				namespace := fmt.Sprintf("team-%d", i%4)
				name := fmt.Sprintf("cluster-%02d", i)
				for iteration := 0; iteration < 20; iteration++ {
					cluster := testCluster(namespace, name)
					cluster.Status.Phase = fmt.Sprintf("phase-%d", iteration)
					if err := st.UpsertCluster(cluster); err != nil {
						t.Errorf("UpsertCluster(%q/%q) error: %v", namespace, name, err)
						return
					}
					if _, ok := st.GetCluster(namespace, name); !ok {
						t.Errorf("GetCluster(%q/%q) found no cluster", namespace, name)
						return
					}
					_ = st.ListClusters("")
				}
			}()
		}
		wg.Wait()

		clusters := st.ListClusters("")
		if got := len(clusters); got != workers {
			t.Fatalf("len(ListClusters(all)) = %d, want %d", got, workers)
		}
	})
}

func nextEvent(t *testing.T, events <-chan ChangeEvent) ChangeEvent {
	t.Helper()

	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for store event")
		return ChangeEvent{}
	}
}

func testCluster(namespace, name string) *cnpgv1.Cluster {
	timestamp := metav1.NewTime(time.Date(2026, time.March, 24, 12, 0, 0, 0, time.UTC))
	return &cnpgv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: timestamp,
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

func testBackup(namespace, name, clusterName string) *cnpgv1.Backup {
	startedAt := metav1.NewTime(time.Date(2026, time.March, 24, 11, 0, 0, 0, time.UTC))
	stoppedAt := metav1.NewTime(time.Date(2026, time.March, 24, 11, 5, 0, 0, time.UTC))
	return &cnpgv1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: startedAt,
		},
		Spec: cnpgv1.BackupSpec{
			Cluster: cnpgv1.LocalObjectReference{Name: clusterName},
			Target:  cnpgv1.BackupTarget("primary"),
			Method:  cnpgv1.BackupMethod("barmanObjectStore"),
		},
		Status: cnpgv1.BackupStatus{
			Phase:     cnpgv1.BackupPhase("completed"),
			Method:    cnpgv1.BackupMethod("barmanObjectStore"),
			StartedAt: &startedAt,
			StoppedAt: &stoppedAt,
		},
	}
}

func testScheduledBackup(namespace, name, clusterName string) *cnpgv1.ScheduledBackup {
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
