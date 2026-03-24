package k8s

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// RestoreCreator creates CNPG Cluster restore resources through the controller-runtime client.
type RestoreCreator struct {
	logger *slog.Logger
	client ctrlclient.Client
}

// NewRestoreCreator constructs a controller-runtime-backed restore creator.
func NewRestoreCreator(bootstrap *ClientBootstrap, logger *slog.Logger) (*RestoreCreator, error) {
	client, err := NewClient(bootstrap)
	if err != nil {
		return nil, err
	}
	return NewRestoreCreatorForClient(client, logger)
}

// NewRestoreCreatorForClient constructs a restore creator around the provided client.
func NewRestoreCreatorForClient(client ctrlclient.Client, logger *slog.Logger) (*RestoreCreator, error) {
	if client == nil {
		return nil, errors.New("kubernetes client is required")
	}
	return &RestoreCreator{logger: loggerOrDiscard(logger), client: client}, nil
}

// CreateCluster creates a restored CNPG Cluster resource for the provided source cluster and backup.
func (c *RestoreCreator) CreateCluster(ctx context.Context, sourceCluster *cnpgv1.Cluster, backup *cnpgv1.Backup, options api.RestoreCreateOptions) (*cnpgv1.Cluster, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("restore creator is not configured")
	}

	restoreCluster, err := buildRestoreCluster(sourceCluster, backup, options)
	if err != nil {
		return nil, err
	}

	if err := c.client.Create(ctx, restoreCluster); err != nil {
		c.logger.Error("create restore cluster resource failed",
			"source_namespace", sourceCluster.Namespace,
			"source_cluster", sourceCluster.Name,
			"backup", backup.Name,
			"target_namespace", restoreCluster.Namespace,
			"target_cluster", restoreCluster.Name,
			"error", err,
		)
		return nil, fmt.Errorf("create restore cluster resource: %w", err)
	}

	c.logger.Info("created restore cluster resource",
		"source_namespace", sourceCluster.Namespace,
		"source_cluster", sourceCluster.Name,
		"backup", backup.Name,
		"backup_method", restoreBackupMethod(backup),
		"target_namespace", restoreCluster.Namespace,
		"target_cluster", restoreCluster.Name,
	)
	return restoreCluster.DeepCopy(), nil
}

func buildRestoreCluster(sourceCluster *cnpgv1.Cluster, backup *cnpgv1.Backup, options api.RestoreCreateOptions) (*cnpgv1.Cluster, error) {
	if sourceCluster == nil {
		return nil, errors.New("source cluster is required")
	}
	if backup == nil {
		return nil, errors.New("backup is required")
	}
	if strings.TrimSpace(options.TargetNamespace) == "" {
		return nil, errors.New("target namespace is required")
	}
	if strings.TrimSpace(options.TargetName) == "" {
		return nil, errors.New("target cluster name is required")
	}

	template := sourceCluster.DeepCopy()
	spec := template.Spec
	spec.Bootstrap = &cnpgv1.BootstrapConfiguration{}
	spec.ReplicaCluster = nil

	database, owner, secret := restoreBootstrapIdentity(sourceCluster)
	recovery := &cnpgv1.BootstrapRecovery{
		Database: database,
		Owner:    owner,
		Secret:   secret,
	}

	switch restoreBackupMethod(backup) {
	case cnpgv1.BackupMethodVolumeSnapshot:
		recovery.Backup = &cnpgv1.BackupSource{LocalObjectReference: cnpgv1.LocalObjectReference{Name: backup.Name}}
	case cnpgv1.BackupMethodBarmanObjectStore:
		recovery.Source = sourceCluster.Name
		recovery.RecoveryTarget = &cnpgv1.RecoveryTarget{BackupID: strings.TrimSpace(backup.Status.BackupID)}
		if strings.TrimSpace(options.PITRTargetTime) != "" {
			recovery.RecoveryTarget.TargetTime = options.PITRTargetTime
		}
		spec.ExternalClusters = upsertExternalCluster(spec.ExternalClusters, restoreExternalCluster(sourceCluster, backup))
	default:
		return nil, fmt.Errorf("backup %q uses unsupported restore method %q", backup.Name, restoreBackupMethod(backup))
	}

	spec.Bootstrap.Recovery = recovery

	restoreCluster := &cnpgv1.Cluster{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cnpgv1.SchemeGroupVersion.String(),
			Kind:       "Cluster",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: strings.TrimSpace(options.TargetNamespace),
			Name:      strings.TrimSpace(options.TargetName),
		},
		Spec: spec,
	}

	return restoreCluster, nil
}

func restoreBootstrapIdentity(cluster *cnpgv1.Cluster) (string, string, *cnpgv1.LocalObjectReference) {
	if cluster == nil || cluster.Spec.Bootstrap == nil {
		return "", "", nil
	}

	switch {
	case cluster.Spec.Bootstrap.InitDB != nil:
		identity := cluster.Spec.Bootstrap.InitDB
		return identity.Database, identity.Owner, identity.Secret
	case cluster.Spec.Bootstrap.Recovery != nil:
		identity := cluster.Spec.Bootstrap.Recovery
		return identity.Database, identity.Owner, identity.Secret
	case cluster.Spec.Bootstrap.PgBaseBackup != nil:
		identity := cluster.Spec.Bootstrap.PgBaseBackup
		return identity.Database, identity.Owner, identity.Secret
	default:
		return "", "", nil
	}
}

func restoreBackupMethod(backup *cnpgv1.Backup) cnpgv1.BackupMethod {
	if backup == nil {
		return ""
	}
	if backup.Status.Method != "" {
		return backup.Status.Method
	}
	return backup.Spec.Method
}

func restoreExternalCluster(sourceCluster *cnpgv1.Cluster, backup *cnpgv1.Backup) cnpgv1.ExternalCluster {
	external := cnpgv1.ExternalCluster{Name: sourceCluster.Name}
	if sourceCluster != nil && sourceCluster.Spec.Backup != nil && sourceCluster.Spec.Backup.BarmanObjectStore != nil {
		copy := sourceCluster.DeepCopy()
		external.BarmanObjectStore = copy.Spec.Backup.BarmanObjectStore
	}
	if external.BarmanObjectStore == nil {
		external.BarmanObjectStore = &cnpgv1.BarmanObjectStoreConfiguration{}
	}

	external.BarmanObjectStore.BarmanCredentials = backup.Status.BarmanCredentials
	if strings.TrimSpace(backup.Status.DestinationPath) != "" {
		external.BarmanObjectStore.DestinationPath = backup.Status.DestinationPath
	}
	if strings.TrimSpace(backup.Status.EndpointURL) != "" {
		external.BarmanObjectStore.EndpointURL = backup.Status.EndpointURL
	}
	if strings.TrimSpace(backup.Status.ServerName) != "" {
		external.BarmanObjectStore.ServerName = backup.Status.ServerName
	}
	if backup.Status.EndpointCA != nil {
		external.BarmanObjectStore.EndpointCA = backup.Status.EndpointCA.DeepCopy()
	}
	return external
}

func upsertExternalCluster(externals []cnpgv1.ExternalCluster, external cnpgv1.ExternalCluster) []cnpgv1.ExternalCluster {
	for idx := range externals {
		if externals[idx].Name == external.Name {
			externals[idx] = external
			return externals
		}
	}
	return append(externals, external)
}

var _ api.RestoreCreator = (*RestoreCreator)(nil)
