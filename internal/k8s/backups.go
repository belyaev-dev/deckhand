package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// BackupCreator creates CNPG Backup resources through the controller-runtime client.
type BackupCreator struct {
	logger *slog.Logger
	client ctrlclient.Client
}

// NewBackupCreator constructs a controller-runtime-backed backup creator.
func NewBackupCreator(bootstrap *ClientBootstrap, logger *slog.Logger) (*BackupCreator, error) {
	client, err := NewClient(bootstrap)
	if err != nil {
		return nil, err
	}
	return NewBackupCreatorForClient(client, logger)
}

// NewBackupCreatorForClient constructs a backup creator around the provided client.
func NewBackupCreatorForClient(client ctrlclient.Client, logger *slog.Logger) (*BackupCreator, error) {
	if client == nil {
		return nil, errors.New("kubernetes client is required")
	}
	return &BackupCreator{logger: loggerOrDiscard(logger), client: client}, nil
}

// CreateBackup creates a generated-name CNPG Backup resource for the provided cluster.
func (c *BackupCreator) CreateBackup(ctx context.Context, cluster *cnpgv1.Cluster, options api.BackupCreateOptions) (*cnpgv1.Backup, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("backup creator is not configured")
	}
	if cluster == nil {
		return nil, errors.New("cluster is required")
	}

	backup := &cnpgv1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    cluster.Namespace,
			GenerateName: generatedBackupNamePrefix(cluster.Name),
		},
		Spec: cnpgv1.BackupSpec{
			Cluster: cnpgv1.LocalObjectReference{Name: cluster.Name},
			Method:  options.Method,
			Target:  options.Target,
		},
	}
	if backup.Spec.Method == "" {
		backup.Spec.Method = cnpgv1.BackupMethodBarmanObjectStore
	}
	if backup.Spec.Target == "" {
		backup.Spec.Target = cnpgv1.DefaultBackupTarget
	}

	if err := c.client.Create(ctx, backup); err != nil {
		c.logger.Error("create backup resource failed",
			"namespace", cluster.Namespace,
			"cluster", cluster.Name,
			"generate_name", backup.GenerateName,
			"method", backup.Spec.Method,
			"target", backup.Spec.Target,
			"error", err,
		)
		return nil, fmt.Errorf("create backup resource: %w", err)
	}

	c.logger.Info("created backup resource",
		"namespace", backup.Namespace,
		"cluster", cluster.Name,
		"backup", backup.Name,
		"generate_name", backup.GenerateName,
		"method", backup.Spec.Method,
		"target", backup.Spec.Target,
	)
	return backup.DeepCopy(), nil
}

func generatedBackupNamePrefix(clusterName string) string {
	clusterName = strings.TrimSpace(clusterName)
	if clusterName == "" {
		return "backup-"
	}
	return clusterName + "-backup-"
}

func loggerOrDiscard(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

var _ api.BackupCreator = (*BackupCreator)(nil)
