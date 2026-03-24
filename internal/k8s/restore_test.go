package k8s

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlclientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestRestoreCreatorCreateCluster(t *testing.T) {
	t.Run("builds a barman restore cluster with yaml-preview-equivalent manifest", func(t *testing.T) {
		scheme, err := NewScheme()
		if err != nil {
			t.Fatalf("NewScheme() error: %v", err)
		}

		var captured *cnpgv1.Cluster
		client := ctrlclientfake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, _ ctrlclient.WithWatch, obj ctrlclient.Object, _ ...ctrlclient.CreateOption) error {
					cluster, ok := obj.(*cnpgv1.Cluster)
					if !ok {
						t.Fatalf("Create() object type = %T, want *cnpgv1.Cluster", obj)
					}
					captured = cluster.DeepCopy()
					return nil
				},
			}).
			Build()

		creator, err := NewRestoreCreatorForClient(client, slog.New(slog.NewJSONHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("NewRestoreCreatorForClient() error: %v", err)
		}

		source := restoreTestSourceClusterWithBarmanConfig("team-a", "alpha")
		backup := restoreTestBarmanBackup("team-a", "alpha-backup-20260324", "alpha")

		created, err := creator.CreateCluster(context.Background(), source, backup, api.RestoreCreateOptions{
			TargetNamespace: "team-b",
			TargetName:      "alpha-restore",
			PITRTargetTime:  "2026-03-24T11:15:00Z",
		})
		if err != nil {
			t.Fatalf("CreateCluster() error: %v", err)
		}
		if captured == nil {
			t.Fatal("CreateCluster() did not issue a client create call")
		}
		if created == nil {
			t.Fatal("CreateCluster() returned nil cluster")
		}
		if got, want := captured.APIVersion, cnpgv1.SchemeGroupVersion.String(); got != want {
			t.Fatalf("captured.APIVersion = %q, want %q", got, want)
		}
		if got, want := captured.Kind, "Cluster"; got != want {
			t.Fatalf("captured.Kind = %q, want %q", got, want)
		}
		if got, want := captured.Namespace, "team-b"; got != want {
			t.Fatalf("captured.Namespace = %q, want %q", got, want)
		}
		if got, want := captured.Name, "alpha-restore"; got != want {
			t.Fatalf("captured.Name = %q, want %q", got, want)
		}
		if captured.Spec.Bootstrap == nil || captured.Spec.Bootstrap.Recovery == nil {
			t.Fatalf("captured.Spec.Bootstrap = %#v, want recovery bootstrap", captured.Spec.Bootstrap)
		}
		if got, want := captured.Spec.Bootstrap.Recovery.Source, "alpha"; got != want {
			t.Fatalf("captured.Spec.Bootstrap.Recovery.Source = %q, want %q", got, want)
		}
		if captured.Spec.Bootstrap.Recovery.Backup != nil {
			t.Fatalf("captured.Spec.Bootstrap.Recovery.Backup = %#v, want nil for barman restore", captured.Spec.Bootstrap.Recovery.Backup)
		}
		if captured.Spec.Bootstrap.Recovery.RecoveryTarget == nil {
			t.Fatal("captured.Spec.Bootstrap.Recovery.RecoveryTarget is nil")
		}
		if got, want := captured.Spec.Bootstrap.Recovery.RecoveryTarget.BackupID, "20260324T110000"; got != want {
			t.Fatalf("captured.Spec.Bootstrap.Recovery.RecoveryTarget.BackupID = %q, want %q", got, want)
		}
		if got, want := captured.Spec.Bootstrap.Recovery.RecoveryTarget.TargetTime, "2026-03-24T11:15:00Z"; got != want {
			t.Fatalf("captured.Spec.Bootstrap.Recovery.RecoveryTarget.TargetTime = %q, want %q", got, want)
		}
		if got, want := captured.Spec.Bootstrap.Recovery.Database, "appdb"; got != want {
			t.Fatalf("captured.Spec.Bootstrap.Recovery.Database = %q, want %q", got, want)
		}
		if got, want := captured.Spec.Bootstrap.Recovery.Owner, "appuser"; got != want {
			t.Fatalf("captured.Spec.Bootstrap.Recovery.Owner = %q, want %q", got, want)
		}
		if captured.Spec.Bootstrap.Recovery.Secret == nil || captured.Spec.Bootstrap.Recovery.Secret.Name != "app-secret" {
			t.Fatalf("captured.Spec.Bootstrap.Recovery.Secret = %#v, want app-secret", captured.Spec.Bootstrap.Recovery.Secret)
		}
		if got := len(captured.Spec.ExternalClusters); got != 1 {
			t.Fatalf("len(captured.Spec.ExternalClusters) = %d, want 1", got)
		}
		external := captured.Spec.ExternalClusters[0]
		if got, want := external.Name, "alpha"; got != want {
			t.Fatalf("external.Name = %q, want %q", got, want)
		}
		if external.BarmanObjectStore == nil {
			t.Fatal("external.BarmanObjectStore is nil")
		}
		if got, want := external.BarmanObjectStore.DestinationPath, "s3://deckhand/backups"; got != want {
			t.Fatalf("external.BarmanObjectStore.DestinationPath = %q, want %q", got, want)
		}
		if got, want := external.BarmanObjectStore.ServerName, "alpha-server"; got != want {
			t.Fatalf("external.BarmanObjectStore.ServerName = %q, want %q", got, want)
		}
		if external.BarmanObjectStore.EndpointCA == nil || external.BarmanObjectStore.EndpointCA.Name != "barman-ca" {
			t.Fatalf("external.BarmanObjectStore.EndpointCA = %#v, want barman-ca", external.BarmanObjectStore.EndpointCA)
		}
		if captured.Spec.ReplicaCluster != nil {
			t.Fatalf("captured.Spec.ReplicaCluster = %#v, want nil", captured.Spec.ReplicaCluster)
		}
		if got, want := created.Name, "alpha-restore"; got != want {
			t.Fatalf("created.Name = %q, want %q", got, want)
		}
	})

	t.Run("uses backup reference for volume snapshot restore", func(t *testing.T) {
		scheme, err := NewScheme()
		if err != nil {
			t.Fatalf("NewScheme() error: %v", err)
		}

		var captured *cnpgv1.Cluster
		client := ctrlclientfake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, _ ctrlclient.WithWatch, obj ctrlclient.Object, _ ...ctrlclient.CreateOption) error {
					cluster, ok := obj.(*cnpgv1.Cluster)
					if !ok {
						t.Fatalf("Create() object type = %T, want *cnpgv1.Cluster", obj)
					}
					captured = cluster.DeepCopy()
					return nil
				},
			}).
			Build()

		creator, err := NewRestoreCreatorForClient(client, slog.New(slog.NewJSONHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("NewRestoreCreatorForClient() error: %v", err)
		}

		source := restoreTestSourceClusterWithSnapshotConfig("team-a", "alpha")
		backup := restoreTestSnapshotBackup("team-a", "alpha-snap-001", "alpha")

		_, err = creator.CreateCluster(context.Background(), source, backup, api.RestoreCreateOptions{
			TargetNamespace: "team-a",
			TargetName:      "alpha-from-snap",
		})
		if err != nil {
			t.Fatalf("CreateCluster() error: %v", err)
		}
		if captured == nil {
			t.Fatal("CreateCluster() did not issue a client create call")
		}
		if captured.Spec.Bootstrap == nil || captured.Spec.Bootstrap.Recovery == nil {
			t.Fatalf("captured.Spec.Bootstrap = %#v, want recovery bootstrap", captured.Spec.Bootstrap)
		}
		if captured.Spec.Bootstrap.Recovery.Backup == nil {
			t.Fatal("captured.Spec.Bootstrap.Recovery.Backup is nil")
		}
		if got, want := captured.Spec.Bootstrap.Recovery.Backup.Name, "alpha-snap-001"; got != want {
			t.Fatalf("captured.Spec.Bootstrap.Recovery.Backup.Name = %q, want %q", got, want)
		}
		if got := captured.Spec.Bootstrap.Recovery.Source; got != "" {
			t.Fatalf("captured.Spec.Bootstrap.Recovery.Source = %q, want empty", got)
		}
		if captured.Spec.Bootstrap.Recovery.RecoveryTarget != nil {
			t.Fatalf("captured.Spec.Bootstrap.Recovery.RecoveryTarget = %#v, want nil", captured.Spec.Bootstrap.Recovery.RecoveryTarget)
		}
		if got := len(captured.Spec.ExternalClusters); got != 0 {
			t.Fatalf("len(captured.Spec.ExternalClusters) = %d, want 0", got)
		}
	})
}

func restoreTestSourceClusterWithBarmanConfig(namespace, name string) *cnpgv1.Cluster {
	return &cnpgv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: cnpgv1.ClusterSpec{
			Instances: 3,
			ImageName: "ghcr.io/cloudnative-pg/postgresql:16.3",
			Backup: &cnpgv1.BackupConfiguration{
				BarmanObjectStore: &cnpgv1.BarmanObjectStoreConfiguration{
					DestinationPath: "s3://deckhand/backups",
					ServerName:      "alpha-server",
					BarmanCredentials: cnpgv1.BarmanCredentials{
						AWS: &cnpgv1.S3Credentials{InheritFromIAMRole: true},
					},
					EndpointCA: &cnpgv1.SecretKeySelector{
						LocalObjectReference: cnpgv1.LocalObjectReference{Name: "barman-ca"},
						Key:                  "ca.crt",
					},
				},
			},
			Bootstrap: &cnpgv1.BootstrapConfiguration{
				InitDB: &cnpgv1.BootstrapInitDB{
					Database: "appdb",
					Owner:    "appuser",
					Secret:   &cnpgv1.LocalObjectReference{Name: "app-secret"},
				},
			},
			ReplicaCluster: &cnpgv1.ReplicaClusterConfiguration{Self: name},
		},
	}
}

func restoreTestSourceClusterWithSnapshotConfig(namespace, name string) *cnpgv1.Cluster {
	cluster := restoreTestSourceClusterWithBarmanConfig(namespace, name)
	cluster.Spec.Backup = &cnpgv1.BackupConfiguration{
		VolumeSnapshot: &cnpgv1.VolumeSnapshotConfiguration{},
	}
	cluster.Spec.ExternalClusters = nil
	return cluster
}

func restoreTestBarmanBackup(namespace, name, clusterName string) *cnpgv1.Backup {
	startedAt := metav1.NewTime(time.Now().UTC().Add(-5 * time.Minute))
	stoppedAt := metav1.NewTime(time.Now().UTC())
	return &cnpgv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: cnpgv1.BackupSpec{
			Cluster: cnpgv1.LocalObjectReference{Name: clusterName},
			Method:  cnpgv1.BackupMethodBarmanObjectStore,
		},
		Status: cnpgv1.BackupStatus{
			Method:          cnpgv1.BackupMethodBarmanObjectStore,
			Phase:           cnpgv1.BackupPhaseCompleted,
			BackupID:        "20260324T110000",
			StartedAt:       &startedAt,
			StoppedAt:       &stoppedAt,
			DestinationPath: "s3://deckhand/backups",
			ServerName:      "alpha-server",
			BarmanCredentials: cnpgv1.BarmanCredentials{
				AWS: &cnpgv1.S3Credentials{InheritFromIAMRole: true},
			},
			EndpointCA: &cnpgv1.SecretKeySelector{
				LocalObjectReference: cnpgv1.LocalObjectReference{Name: "barman-ca"},
				Key:                  "ca.crt",
			},
		},
	}
}

func restoreTestSnapshotBackup(namespace, name, clusterName string) *cnpgv1.Backup {
	return &cnpgv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: cnpgv1.BackupSpec{
			Cluster: cnpgv1.LocalObjectReference{Name: clusterName},
			Method:  cnpgv1.BackupMethodVolumeSnapshot,
		},
		Status: cnpgv1.BackupStatus{
			Method: cnpgv1.BackupMethodVolumeSnapshot,
			Phase:  cnpgv1.BackupPhaseCompleted,
		},
	}
}
