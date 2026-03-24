package api

import (
	"regexp"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/metrics"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var ipAddressPattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// ClusterListResponse is the stable JSON contract for GET /api/clusters.
type ClusterListResponse struct {
	Namespaces []ClusterNamespaceSummary `json:"namespaces"`
	Items      []ClusterOverviewSummary  `json:"items"`
}

// ClusterNamespaceSummary exposes the namespaces available in the current list response.
type ClusterNamespaceSummary struct {
	Name         string `json:"name"`
	ClusterCount int    `json:"clusterCount"`
}

// ClusterOverviewSummary exposes overview-ready cluster fields for the list page.
type ClusterOverviewSummary struct {
	ClusterSummary
	OverallHealth      string     `json:"overallHealth"`
	MetricsScrapedAt   *time.Time `json:"metricsScrapedAt,omitempty"`
	MetricsScrapeError string     `json:"metricsScrapeError"`
}

// ClusterDetailResponse is the stable JSON contract for GET /api/clusters/{namespace}/{name}.
type ClusterDetailResponse struct {
	Cluster          ClusterSummary           `json:"cluster"`
	Backups          []BackupSummary          `json:"backups"`
	ScheduledBackups []ScheduledBackupSummary `json:"scheduledBackups"`
}

// ClusterBackupsResponse is the stable JSON contract for GET /api/clusters/{namespace}/{name}/backups.
type ClusterBackupsResponse struct {
	Cluster          ClusterSummary           `json:"cluster"`
	Backups          []BackupSummary          `json:"backups"`
	ScheduledBackups []ScheduledBackupSummary `json:"scheduledBackups"`
}

// CreateBackupRequest is the stable JSON contract for POST /api/clusters/{namespace}/{name}/backups.
type CreateBackupRequest struct {
	Method cnpgv1.BackupMethod `json:"method,omitempty"`
	Target cnpgv1.BackupTarget `json:"target,omitempty"`
}

// CreateBackupResponse is the stable JSON contract for POST /api/clusters/{namespace}/{name}/backups.
type CreateBackupResponse struct {
	Backup BackupSummary `json:"backup"`
}

// ClusterRestoreOptionsResponse is the stable JSON contract for GET /api/clusters/{namespace}/{name}/restore.
type ClusterRestoreOptionsResponse struct {
	Cluster         ClusterSummary              `json:"cluster"`
	Backups         []BackupSummary             `json:"backups"`
	Recoverability  RestoreRecoverabilityWindow `json:"recoverability"`
	SupportedPhases []string                    `json:"supportedPhases"`
}

// RestoreRecoverabilityWindow exposes the source cluster's advertised PITR window.
type RestoreRecoverabilityWindow struct {
	Start *time.Time `json:"start,omitempty"`
	End   *time.Time `json:"end,omitempty"`
}

// CreateRestoreRequest is the stable JSON contract for POST /api/clusters/{namespace}/{name}/restore.
type CreateRestoreRequest struct {
	BackupName      string `json:"backupName"`
	TargetNamespace string `json:"targetNamespace"`
	TargetName      string `json:"targetName"`
	PITRTargetTime  string `json:"pitrTargetTime,omitempty"`
}

// CreateRestoreResponse is the stable JSON contract for POST /api/clusters/{namespace}/{name}/restore.
type CreateRestoreResponse struct {
	SourceCluster ClusterSummary `json:"sourceCluster"`
	TargetCluster ClusterSummary `json:"targetCluster"`
	Backup        BackupSummary  `json:"backup"`
	YAMLPreview   string         `json:"yamlPreview"`
	RestoreStatus RestoreStatus  `json:"restoreStatus"`
}

// RestoreStatusResponse is the stable JSON contract for GET /api/clusters/{namespace}/{name}/restore-status.
type RestoreStatusResponse struct {
	Cluster ClusterSummary `json:"cluster"`
	Status  RestoreStatus  `json:"status"`
}

// RestoreStatus exposes the guided-restore progress model for a target cluster.
type RestoreStatus struct {
	Phase       string                 `json:"phase"`
	PhaseReason string                 `json:"phaseReason,omitempty"`
	Message     string                 `json:"message,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Timestamps  RestorePhaseTimestamps `json:"timestamps"`
}

// RestorePhaseTimestamps exposes timeline-friendly timestamps for each restore phase.
type RestorePhaseTimestamps struct {
	BootstrappingStartedAt *time.Time `json:"bootstrappingStartedAt,omitempty"`
	RecoveringStartedAt    *time.Time `json:"recoveringStartedAt,omitempty"`
	ReadyAt                *time.Time `json:"readyAt,omitempty"`
	FailedAt               *time.Time `json:"failedAt,omitempty"`
	LastTransitionAt       *time.Time `json:"lastTransitionAt,omitempty"`
}

// ClusterMetricsResponse is the stable JSON contract for GET /api/clusters/{namespace}/{name}/metrics.
type ClusterMetricsResponse struct {
	Cluster       ClusterSummary           `json:"cluster"`
	OverallHealth string                   `json:"overallHealth"`
	ScrapedAt     *time.Time               `json:"scrapedAt,omitempty"`
	ScrapeError   string                   `json:"scrapeError,omitempty"`
	Instances     []InstanceMetricsSummary `json:"instances"`
}

// ErrorResponse is the stable JSON contract for API errors.
type ErrorResponse struct {
	Error string `json:"error"`
}

// ClusterSummary exposes frontend-safe Cluster fields without leaking raw CRDs.
type ClusterSummary struct {
	Namespace                string     `json:"namespace"`
	Name                     string     `json:"name"`
	CreatedAt                *time.Time `json:"createdAt,omitempty"`
	Phase                    string     `json:"phase,omitempty"`
	PhaseReason              string     `json:"phaseReason,omitempty"`
	DesiredInstances         int        `json:"desiredInstances"`
	ReadyInstances           int        `json:"readyInstances"`
	CurrentPrimary           string     `json:"currentPrimary,omitempty"`
	Image                    string     `json:"image,omitempty"`
	FirstRecoverabilityPoint *time.Time `json:"firstRecoverabilityPoint,omitempty"`
	LastSuccessfulBackup     *time.Time `json:"lastSuccessfulBackup,omitempty"`
}

// BackupSummary exposes frontend-safe Backup fields without leaking credentials.
type BackupSummary struct {
	Namespace   string     `json:"namespace"`
	Name        string     `json:"name"`
	ClusterName string     `json:"clusterName"`
	CreatedAt   *time.Time `json:"createdAt,omitempty"`
	Phase       string     `json:"phase,omitempty"`
	Method      string     `json:"method,omitempty"`
	Target      string     `json:"target,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	StoppedAt   *time.Time `json:"stoppedAt,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// ScheduledBackupSummary exposes frontend-safe ScheduledBackup fields.
type ScheduledBackupSummary struct {
	Namespace        string     `json:"namespace"`
	Name             string     `json:"name"`
	ClusterName      string     `json:"clusterName"`
	CreatedAt        *time.Time `json:"createdAt,omitempty"`
	Schedule         string     `json:"schedule"`
	Method           string     `json:"method,omitempty"`
	Target           string     `json:"target,omitempty"`
	Immediate        bool       `json:"immediate"`
	Suspended        bool       `json:"suspended"`
	LastScheduleTime *time.Time `json:"lastScheduleTime,omitempty"`
	NextScheduleTime *time.Time `json:"nextScheduleTime,omitempty"`
}

// InstanceMetricsSummary exposes frontend-safe per-pod metrics without leaking pod IPs.
type InstanceMetricsSummary struct {
	PodName     string                    `json:"podName"`
	PodStatus   string                    `json:"podStatus,omitempty"`
	Health      string                    `json:"health"`
	Connections ConnectionMetricsSummary  `json:"connections"`
	Replication ReplicationMetricsSummary `json:"replication"`
	Disk        DiskMetricsSummary        `json:"disk"`
	ScrapedAt   *time.Time                `json:"scrapedAt,omitempty"`
	ScrapeError string                    `json:"scrapeError,omitempty"`
}

// ConnectionMetricsSummary exposes frontend-safe connection counters.
type ConnectionMetricsSummary struct {
	Active            int `json:"active"`
	Idle              int `json:"idle"`
	IdleInTransaction int `json:"idleInTransaction"`
	Total             int `json:"total"`
	MaxConnections    int `json:"maxConnections"`
}

// ReplicationMetricsSummary exposes frontend-safe replication state.
type ReplicationMetricsSummary struct {
	ReplicationLagSeconds float64 `json:"replicationLagSeconds"`
	IsReplica             bool    `json:"isReplica"`
	IsWALReceiverUp       bool    `json:"isWalReceiverUp"`
	StreamingReplicas     int     `json:"streamingReplicas"`
	ReplayLagBytes        float64 `json:"replayLagBytes"`
}

// DiskMetricsSummary exposes frontend-safe PVC/database sizing.
type DiskMetricsSummary struct {
	PVCCapacityBytes  int64 `json:"pvcCapacityBytes"`
	DatabaseSizeBytes int64 `json:"databaseSizeBytes"`
}

const (
	restorePhaseBootstrapping = "bootstrapping"
	restorePhaseRecovering    = "recovering"
	restorePhaseReady         = "ready"
	restorePhaseFailed        = "failed"
)

var restoreSupportedPhases = []string{
	restorePhaseBootstrapping,
	restorePhaseRecovering,
	restorePhaseReady,
	restorePhaseFailed,
}

func newClusterSummary(cluster *cnpgv1.Cluster) ClusterSummary {
	if cluster == nil {
		return ClusterSummary{}
	}

	return ClusterSummary{
		Namespace:                cluster.Namespace,
		Name:                     cluster.Name,
		CreatedAt:                metaTime(cluster.CreationTimestamp),
		Phase:                    cluster.Status.Phase,
		PhaseReason:              cluster.Status.PhaseReason,
		DesiredInstances:         cluster.Spec.Instances,
		ReadyInstances:           cluster.Status.ReadyInstances,
		CurrentPrimary:           cluster.Status.CurrentPrimary,
		Image:                    clusterImage(cluster),
		FirstRecoverabilityPoint: firstRecoverabilityPoint(cluster),
		LastSuccessfulBackup:     lastSuccessfulBackup(cluster),
	}
}

func newClusterRestoreOptionsResponse(cluster *cnpgv1.Cluster, backups []*cnpgv1.Backup) ClusterRestoreOptionsResponse {
	response := ClusterRestoreOptionsResponse{
		Cluster: newClusterSummary(cluster),
		Backups: make([]BackupSummary, 0, len(backups)),
		Recoverability: RestoreRecoverabilityWindow{
			Start: firstRecoverabilityPoint(cluster),
			End:   lastSuccessfulBackup(cluster),
		},
		SupportedPhases: append([]string(nil), restoreSupportedPhases...),
	}
	for _, backup := range backups {
		response.Backups = append(response.Backups, newBackupSummary(backup))
	}
	return response
}

func newClusterOverviewSummary(cluster *cnpgv1.Cluster, snapshot *metrics.ClusterMetrics) ClusterOverviewSummary {
	overview := ClusterOverviewSummary{
		ClusterSummary:     newClusterSummary(cluster),
		OverallHealth:      healthStatusString(metrics.Unknown),
		MetricsScrapeError: "metrics not available yet",
	}
	if snapshot == nil {
		return overview
	}

	overview.OverallHealth = healthStatusString(snapshot.OverallHealth)
	overview.MetricsScrapedAt = timePtr(snapshot.ScrapedAt)
	overview.MetricsScrapeError = redactDiagnostic(snapshot.ScrapeError)
	return overview
}

func newClusterMetricsResponse(cluster *cnpgv1.Cluster, snapshot *metrics.ClusterMetrics) ClusterMetricsResponse {
	response := ClusterMetricsResponse{
		Cluster:       newClusterSummary(cluster),
		OverallHealth: healthStatusString(metrics.Unknown),
		ScrapeError:   "metrics not available yet",
		Instances:     []InstanceMetricsSummary{},
	}
	if snapshot == nil {
		return response
	}

	response.OverallHealth = healthStatusString(snapshot.OverallHealth)
	response.ScrapedAt = timePtr(snapshot.ScrapedAt)
	response.ScrapeError = redactDiagnostic(snapshot.ScrapeError)
	response.Instances = make([]InstanceMetricsSummary, 0, len(snapshot.Instances))
	podStatuses := instancePodStatuses(cluster)
	for _, instance := range snapshot.Instances {
		response.Instances = append(response.Instances, newInstanceMetricsSummary(instance, podStatuses[instance.PodName]))
	}

	return response
}

func newBackupSummary(backup *cnpgv1.Backup) BackupSummary {
	if backup == nil {
		return BackupSummary{}
	}

	method := string(backup.Status.Method)
	if method == "" {
		method = string(backup.Spec.Method)
	}

	return BackupSummary{
		Namespace:   backup.Namespace,
		Name:        backup.Name,
		ClusterName: backup.Spec.Cluster.Name,
		CreatedAt:   metaTime(backup.CreationTimestamp),
		Phase:       string(backup.Status.Phase),
		Method:      method,
		Target:      string(backup.Spec.Target),
		StartedAt:   metaTimePtr(backup.Status.StartedAt),
		StoppedAt:   metaTimePtr(backup.Status.StoppedAt),
		Error:       backup.Status.Error,
	}
}

func newScheduledBackupSummary(scheduledBackup *cnpgv1.ScheduledBackup) ScheduledBackupSummary {
	if scheduledBackup == nil {
		return ScheduledBackupSummary{}
	}

	return ScheduledBackupSummary{
		Namespace:        scheduledBackup.Namespace,
		Name:             scheduledBackup.Name,
		ClusterName:      scheduledBackup.Spec.Cluster.Name,
		CreatedAt:        metaTime(scheduledBackup.CreationTimestamp),
		Schedule:         scheduledBackup.Spec.Schedule,
		Method:           string(scheduledBackup.Spec.Method),
		Target:           string(scheduledBackup.Spec.Target),
		Immediate:        scheduledBackup.IsImmediate(),
		Suspended:        scheduledBackup.IsSuspended(),
		LastScheduleTime: metaTimePtr(scheduledBackup.Status.LastScheduleTime),
		NextScheduleTime: metaTimePtr(scheduledBackup.Status.NextScheduleTime),
	}
}

func newInstanceMetricsSummary(instance metrics.InstanceMetrics, podStatus string) InstanceMetricsSummary {
	return InstanceMetricsSummary{
		PodName:   instance.PodName,
		PodStatus: podStatus,
		Health:    healthStatusString(instance.Health),
		Connections: ConnectionMetricsSummary{
			Active:            instance.Connections.Active,
			Idle:              instance.Connections.Idle,
			IdleInTransaction: instance.Connections.IdleInTransaction,
			Total:             instance.Connections.Total,
			MaxConnections:    instance.Connections.MaxConnections,
		},
		Replication: ReplicationMetricsSummary{
			ReplicationLagSeconds: instance.Replication.ReplicationLagSeconds,
			IsReplica:             instance.Replication.IsReplica,
			IsWALReceiverUp:       instance.Replication.IsWALReceiverUp,
			StreamingReplicas:     instance.Replication.StreamingReplicas,
			ReplayLagBytes:        instance.Replication.ReplayLagBytes,
		},
		Disk: DiskMetricsSummary{
			PVCCapacityBytes:  instance.Disk.PVCCapacityBytes,
			DatabaseSizeBytes: instance.Disk.DatabaseSizeBytes,
		},
		ScrapedAt:   timePtr(instance.ScrapedAt),
		ScrapeError: redactDiagnostic(instance.ScrapeError),
	}
}

func instancePodStatuses(cluster *cnpgv1.Cluster) map[string]string {
	if cluster == nil || len(cluster.Status.InstancesStatus) == 0 {
		return map[string]string{}
	}

	statuses := make(map[string]string, len(cluster.Status.InstanceNames))
	for status, podNames := range cluster.Status.InstancesStatus {
		for _, podName := range podNames {
			statuses[podName] = string(status)
		}
	}
	return statuses
}

func clusterImage(cluster *cnpgv1.Cluster) string {
	if cluster.Status.Image != "" {
		return cluster.Status.Image
	}
	return cluster.Spec.ImageName
}

func firstRecoverabilityPoint(cluster *cnpgv1.Cluster) *time.Time {
	if cluster == nil {
		return nil
	}

	var earliest *time.Time
	for _, value := range cluster.Status.FirstRecoverabilityPointByMethod {
		candidate := value.Time.UTC()
		if earliest == nil || candidate.Before(*earliest) {
			candidateCopy := candidate
			earliest = &candidateCopy
		}
	}
	if earliest != nil {
		return earliest
	}
	return parseTimestamp(cluster.Status.FirstRecoverabilityPoint)
}

func lastSuccessfulBackup(cluster *cnpgv1.Cluster) *time.Time {
	if cluster == nil {
		return nil
	}

	var latest *time.Time
	for _, value := range cluster.Status.LastSuccessfulBackupByMethod {
		candidate := value.Time.UTC()
		if latest == nil || candidate.After(*latest) {
			candidateCopy := candidate
			latest = &candidateCopy
		}
	}
	if latest != nil {
		return latest
	}
	return parseTimestamp(cluster.Status.LastSuccessfulBackup)
}

func metaTime(value metav1.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	timestamp := value.Time.UTC()
	return &timestamp
}

func metaTimePtr(value *metav1.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	timestamp := value.Time.UTC()
	return &timestamp
}

func parseTimestamp(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	timestamp := value.UTC()
	return &timestamp
}

func healthStatusString(status metrics.HealthStatus) string {
	if status == "" {
		return string(metrics.Unknown)
	}
	return string(status)
}

func redactDiagnostic(message string) string {
	message = ipAddressPattern.ReplaceAllString(message, "<redacted>")
	return message
}
