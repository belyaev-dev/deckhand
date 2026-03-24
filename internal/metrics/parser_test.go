package metrics

import (
	"strings"
	"testing"
)

func TestParseMetrics_FullOutput(t *testing.T) {
	t.Parallel()

	input := strings.NewReader(`
# HELP cnpg_backends_total Number of PostgreSQL backends by state.
# TYPE cnpg_backends_total gauge
cnpg_backends_total{state="active"} 4
cnpg_backends_total{state="idle"} 6
cnpg_backends_total{state="idle in transaction"} 2
# HELP cnpg_pg_replication_lag Replication lag in seconds.
# TYPE cnpg_pg_replication_lag gauge
cnpg_pg_replication_lag 12.5
# HELP cnpg_pg_replication_in_recovery Replica state.
# TYPE cnpg_pg_replication_in_recovery gauge
cnpg_pg_replication_in_recovery 1
# HELP cnpg_pg_replication_is_wal_receiver_up WAL receiver state.
# TYPE cnpg_pg_replication_is_wal_receiver_up gauge
cnpg_pg_replication_is_wal_receiver_up 1
# HELP cnpg_pg_replication_streaming_replicas Streaming replicas.
# TYPE cnpg_pg_replication_streaming_replicas gauge
cnpg_pg_replication_streaming_replicas 3
# HELP cnpg_pg_stat_replication_replay_diff_bytes Replay lag bytes.
# TYPE cnpg_pg_stat_replication_replay_diff_bytes gauge
cnpg_pg_stat_replication_replay_diff_bytes{application_name="alpha-2"} 1024
cnpg_pg_stat_replication_replay_diff_bytes{application_name="alpha-3"} 4096
`)

	metrics, err := ParseMetrics(input)
	if err != nil {
		t.Fatalf("ParseMetrics() error = %v", err)
	}

	if metrics == nil {
		t.Fatal("ParseMetrics() returned nil metrics")
	}
	if got, want := metrics.Connections.Active, 4; got != want {
		t.Fatalf("Connections.Active = %d, want %d", got, want)
	}
	if got, want := metrics.Connections.Idle, 6; got != want {
		t.Fatalf("Connections.Idle = %d, want %d", got, want)
	}
	if got, want := metrics.Connections.IdleInTransaction, 2; got != want {
		t.Fatalf("Connections.IdleInTransaction = %d, want %d", got, want)
	}
	if got, want := metrics.Connections.Total, 12; got != want {
		t.Fatalf("Connections.Total = %d, want %d", got, want)
	}
	if got, want := metrics.Replication.ReplicationLagSeconds, 12.5; got != want {
		t.Fatalf("Replication.ReplicationLagSeconds = %v, want %v", got, want)
	}
	if got, want := metrics.Replication.IsReplica, true; got != want {
		t.Fatalf("Replication.IsReplica = %t, want %t", got, want)
	}
	if got, want := metrics.Replication.IsWALReceiverUp, true; got != want {
		t.Fatalf("Replication.IsWALReceiverUp = %t, want %t", got, want)
	}
	if got, want := metrics.Replication.StreamingReplicas, 3; got != want {
		t.Fatalf("Replication.StreamingReplicas = %d, want %d", got, want)
	}
	if got, want := metrics.Replication.ReplayLagBytes, 4096.0; got != want {
		t.Fatalf("Replication.ReplayLagBytes = %v, want %v", got, want)
	}
}

func TestParseMetrics_MissingFamilies(t *testing.T) {
	t.Parallel()

	input := strings.NewReader(`
# TYPE cnpg_backends_total gauge
cnpg_backends_total{state="active"} 3
cnpg_backends_total{state="idle"} 5
`)

	metrics, err := ParseMetrics(input)
	if err != nil {
		t.Fatalf("ParseMetrics() error = %v", err)
	}

	if got, want := metrics.Connections.Total, 8; got != want {
		t.Fatalf("Connections.Total = %d, want %d", got, want)
	}
	if got := metrics.Replication.ReplicationLagSeconds; got != 0 {
		t.Fatalf("Replication.ReplicationLagSeconds = %v, want 0", got)
	}
	if got := metrics.Replication.StreamingReplicas; got != 0 {
		t.Fatalf("Replication.StreamingReplicas = %d, want 0", got)
	}
	if metrics.Replication.IsReplica {
		t.Fatal("Replication.IsReplica = true, want false")
	}
	if metrics.Replication.IsWALReceiverUp {
		t.Fatal("Replication.IsWALReceiverUp = true, want false")
	}
	if got := metrics.Replication.ReplayLagBytes; got != 0 {
		t.Fatalf("Replication.ReplayLagBytes = %v, want 0", got)
	}
}

func TestParseMetrics_EmptyInput(t *testing.T) {
	t.Parallel()

	metrics, err := ParseMetrics(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseMetrics() error = %v", err)
	}
	if metrics == nil {
		t.Fatal("ParseMetrics() returned nil metrics")
	}
	if got := metrics.Connections.Total; got != 0 {
		t.Fatalf("Connections.Total = %d, want 0", got)
	}
	if got := metrics.Replication.ReplicationLagSeconds; got != 0 {
		t.Fatalf("Replication.ReplicationLagSeconds = %v, want 0", got)
	}
}

func TestParseMetrics_MalformedInput(t *testing.T) {
	t.Parallel()

	_, err := ParseMetrics(strings.NewReader(`
# TYPE cnpg_backends_total gauge
cnpg_backends_total{state="active" 4
`))
	if err == nil {
		t.Fatal("ParseMetrics() error = nil, want non-nil")
	}
}

func TestEvaluateHealth_Healthy(t *testing.T) {
	t.Parallel()

	status := EvaluateHealth(&InstanceMetrics{
		Connections: ConnectionMetrics{Total: 80, MaxConnections: 100},
		Replication: ReplicationMetrics{ReplicationLagSeconds: 10},
	}, DefaultThresholds())

	if got, want := status, Healthy; got != want {
		t.Fatalf("EvaluateHealth() = %q, want %q", got, want)
	}
}

func TestEvaluateHealth_Warning(t *testing.T) {
	t.Parallel()

	status := EvaluateHealth(&InstanceMetrics{
		Replication: ReplicationMetrics{ReplicationLagSeconds: 15},
	}, DefaultThresholds())

	if got, want := status, Warning; got != want {
		t.Fatalf("EvaluateHealth() = %q, want %q", got, want)
	}
}

func TestEvaluateHealth_Critical(t *testing.T) {
	t.Parallel()

	status := EvaluateHealth(&InstanceMetrics{
		Replication: ReplicationMetrics{ReplicationLagSeconds: 45},
	}, DefaultThresholds())

	if got, want := status, Critical; got != want {
		t.Fatalf("EvaluateHealth() = %q, want %q", got, want)
	}
}

func TestEvaluateHealth_ConnectionWarning(t *testing.T) {
	t.Parallel()

	status := EvaluateHealth(&InstanceMetrics{
		Connections: ConnectionMetrics{Total: 85, MaxConnections: 100},
	}, DefaultThresholds())

	if got, want := status, Warning; got != want {
		t.Fatalf("EvaluateHealth() = %q, want %q", got, want)
	}
}

func TestDefaultThresholds_IncludeDiskUsageRatios(t *testing.T) {
	t.Parallel()

	thresholds := DefaultThresholds()
	if got, want := thresholds.DiskUsageRatioWarning, 0.80; got != want {
		t.Fatalf("DefaultThresholds().DiskUsageRatioWarning = %v, want %v", got, want)
	}
	if got, want := thresholds.DiskUsageRatioCritical, 0.90; got != want {
		t.Fatalf("DefaultThresholds().DiskUsageRatioCritical = %v, want %v", got, want)
	}
}

func TestEvaluateHealth_DiskWarning(t *testing.T) {
	t.Parallel()

	status := EvaluateHealth(&InstanceMetrics{
		Disk: DiskMetrics{
			PVCCapacityBytes:  100,
			DatabaseSizeBytes: 81,
		},
	}, DefaultThresholds())

	if got, want := status, Warning; got != want {
		t.Fatalf("EvaluateHealth() = %q, want %q", got, want)
	}
}

func TestEvaluateHealth_DiskCritical(t *testing.T) {
	t.Parallel()

	status := EvaluateHealth(&InstanceMetrics{
		Disk: DiskMetrics{
			PVCCapacityBytes:  100,
			DatabaseSizeBytes: 91,
		},
	}, DefaultThresholds())

	if got, want := status, Critical; got != want {
		t.Fatalf("EvaluateHealth() = %q, want %q", got, want)
	}
}

func TestEvaluateHealth_SkipsDiskWhenCapacityUnknown(t *testing.T) {
	t.Parallel()

	status := EvaluateHealth(&InstanceMetrics{
		Disk: DiskMetrics{
			PVCCapacityBytes:  0,
			DatabaseSizeBytes: 91,
		},
	}, DefaultThresholds())

	if got, want := status, Healthy; got != want {
		t.Fatalf("EvaluateHealth() = %q, want %q", got, want)
	}
}

func TestThreshold_Healthy(t *testing.T) {
	t.Parallel()

	status := EvaluateHealth(&InstanceMetrics{
		Connections: ConnectionMetrics{Total: 80, MaxConnections: 100},
		Replication: ReplicationMetrics{ReplicationLagSeconds: 10},
	}, DefaultThresholds())

	if got, want := status, Healthy; got != want {
		t.Fatalf("EvaluateHealth() = %q, want %q", got, want)
	}
}

func TestThreshold_Warning(t *testing.T) {
	t.Parallel()

	status := EvaluateHealth(&InstanceMetrics{
		Replication: ReplicationMetrics{ReplicationLagSeconds: 15},
	}, DefaultThresholds())

	if got, want := status, Warning; got != want {
		t.Fatalf("EvaluateHealth() = %q, want %q", got, want)
	}
}

func TestThreshold_Critical(t *testing.T) {
	t.Parallel()

	status := EvaluateHealth(&InstanceMetrics{
		Replication: ReplicationMetrics{ReplicationLagSeconds: 45},
	}, DefaultThresholds())

	if got, want := status, Critical; got != want {
		t.Fatalf("EvaluateHealth() = %q, want %q", got, want)
	}
}

func TestThreshold_ConnectionWarning(t *testing.T) {
	t.Parallel()

	status := EvaluateHealth(&InstanceMetrics{
		Connections: ConnectionMetrics{Total: 85, MaxConnections: 100},
	}, DefaultThresholds())

	if got, want := status, Warning; got != want {
		t.Fatalf("EvaluateHealth() = %q, want %q", got, want)
	}
}

func TestAggregateClusterHealth(t *testing.T) {
	t.Parallel()

	status := AggregateClusterHealth([]InstanceMetrics{
		{Health: Healthy},
		{Health: Warning},
		{Health: Critical},
	})

	if got, want := status, Critical; got != want {
		t.Fatalf("AggregateClusterHealth() = %q, want %q", got, want)
	}
}
