package metrics

import "time"

// HealthStatus represents the threshold-evaluated health of a metric set.
type HealthStatus string

const (
	Healthy  HealthStatus = "healthy"
	Warning  HealthStatus = "warning"
	Critical HealthStatus = "critical"
	Unknown  HealthStatus = "unknown"
)

// ConnectionMetrics captures connection pool usage extracted from CNPG metrics.
type ConnectionMetrics struct {
	Active            int
	Idle              int
	IdleInTransaction int
	Total             int
	MaxConnections    int
}

// ReplicationMetrics captures replication state extracted from CNPG metrics.
type ReplicationMetrics struct {
	ReplicationLagSeconds float64
	IsReplica             bool
	IsWALReceiverUp       bool
	StreamingReplicas     int
	ReplayLagBytes        float64
}

// DiskMetrics captures PVC and database sizing information for an instance.
type DiskMetrics struct {
	PVCCapacityBytes  int64
	DatabaseSizeBytes int64
}

// InstanceMetrics contains the typed metrics snapshot for a single CNPG pod.
type InstanceMetrics struct {
	PodName     string
	Connections ConnectionMetrics
	Replication ReplicationMetrics
	Disk        DiskMetrics
	Health      HealthStatus
	ScrapeError string
	ScrapedAt   time.Time
}

// ClusterMetrics contains metrics snapshots for every instance in a cluster.
type ClusterMetrics struct {
	Namespace     string
	ClusterName   string
	Instances     []InstanceMetrics
	OverallHealth HealthStatus
	ScrapeError   string
	ScrapedAt     time.Time
}

// HealthThresholds holds configurable warning and critical boundaries.
type HealthThresholds struct {
	ReplicationLagWarningSeconds  float64
	ReplicationLagCriticalSeconds float64
	ConnectionRatioWarning        float64
	ConnectionRatioCritical       float64
	DiskUsageRatioWarning         float64
	DiskUsageRatioCritical        float64
}

// DefaultThresholds returns the baseline thresholds for runtime evaluation.
func DefaultThresholds() HealthThresholds {
	return HealthThresholds{
		ReplicationLagWarningSeconds:  10,
		ReplicationLagCriticalSeconds: 30,
		ConnectionRatioWarning:        0.8,
		ConnectionRatioCritical:       0.9,
		DiskUsageRatioWarning:         0.80,
		DiskUsageRatioCritical:        0.90,
	}
}

// EvaluateHealth returns the worst health classification implied by the metrics.
func EvaluateHealth(metrics *InstanceMetrics, thresholds HealthThresholds) HealthStatus {
	if metrics == nil {
		return Unknown
	}

	status := Healthy
	if metrics.ScrapeError != "" {
		status = Unknown
	}

	if metrics.Replication.ReplicationLagSeconds > thresholds.ReplicationLagCriticalSeconds {
		status = worseStatus(status, Critical)
	} else if metrics.Replication.ReplicationLagSeconds > thresholds.ReplicationLagWarningSeconds {
		status = worseStatus(status, Warning)
	}

	if metrics.Connections.MaxConnections > 0 {
		ratio := float64(metrics.Connections.Total) / float64(metrics.Connections.MaxConnections)
		if ratio > thresholds.ConnectionRatioCritical {
			status = worseStatus(status, Critical)
		} else if ratio > thresholds.ConnectionRatioWarning {
			status = worseStatus(status, Warning)
		}
	}

	if metrics.Disk.PVCCapacityBytes > 0 {
		ratio := float64(metrics.Disk.DatabaseSizeBytes) / float64(metrics.Disk.PVCCapacityBytes)
		if ratio > thresholds.DiskUsageRatioCritical {
			status = worseStatus(status, Critical)
		} else if ratio > thresholds.DiskUsageRatioWarning {
			status = worseStatus(status, Warning)
		}
	}

	return status
}

// AggregateClusterHealth returns the worst health classification across instances.
func AggregateClusterHealth(instances []InstanceMetrics) HealthStatus {
	if len(instances) == 0 {
		return Unknown
	}

	status := Healthy
	for _, instance := range instances {
		status = worseStatus(status, instance.Health)
	}
	return status
}

func worseStatus(current, candidate HealthStatus) HealthStatus {
	if healthRank(candidate) > healthRank(current) {
		return candidate
	}
	return current
}

func healthRank(status HealthStatus) int {
	switch status {
	case Healthy:
		return 0
	case Unknown:
		return 1
	case Warning:
		return 2
	case Critical:
		return 3
	default:
		return 1
	}
}
