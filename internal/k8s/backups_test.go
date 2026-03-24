package k8s

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/api"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlclientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestBackupCreatorCreateBackup(t *testing.T) {
	scheme, err := NewScheme()
	if err != nil {
		t.Fatalf("NewScheme() error: %v", err)
	}

	var captured *cnpgv1.Backup
	client := ctrlclientfake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(_ context.Context, _ ctrlclient.WithWatch, obj ctrlclient.Object, _ ...ctrlclient.CreateOption) error {
				backup, ok := obj.(*cnpgv1.Backup)
				if !ok {
					t.Fatalf("Create() object type = %T, want *cnpgv1.Backup", obj)
				}
				captured = backup.DeepCopy()
				backup.Name = "alpha-backup-manual-001"
				return nil
			},
		}).
		Build()

	creator, err := NewBackupCreatorForClient(client, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewBackupCreatorForClient() error: %v", err)
	}

	cluster := &cnpgv1.Cluster{}
	cluster.Namespace = "team-a"
	cluster.Name = "alpha"

	created, err := creator.CreateBackup(context.Background(), cluster, api.BackupCreateOptions{})
	if err != nil {
		t.Fatalf("CreateBackup() error: %v", err)
	}
	if captured == nil {
		t.Fatal("CreateBackup() did not issue a client create call")
	}
	if created == nil {
		t.Fatal("CreateBackup() returned nil backup")
	}
	if got, want := created.Name, "alpha-backup-manual-001"; got != want {
		t.Fatalf("created.Name = %q, want %q", got, want)
	}
	if got, want := captured.Namespace, "team-a"; got != want {
		t.Fatalf("captured.Namespace = %q, want %q", got, want)
	}
	if got, want := captured.Spec.Cluster.Name, "alpha"; got != want {
		t.Fatalf("captured.Spec.Cluster.Name = %q, want %q", got, want)
	}
	if got, want := captured.Spec.Method, cnpgv1.BackupMethodBarmanObjectStore; got != want {
		t.Fatalf("captured.Spec.Method = %q, want %q", got, want)
	}
	if got, want := captured.Spec.Target, cnpgv1.DefaultBackupTarget; got != want {
		t.Fatalf("captured.Spec.Target = %q, want %q", got, want)
	}
	if !strings.HasPrefix(captured.GenerateName, "alpha-backup-") {
		t.Fatalf("captured.GenerateName = %q, want alpha-backup-*", captured.GenerateName)
	}
}
