package metrics

import (
	"io"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

const (
	metricBackendsTotal                = "cnpg_backends_total"
	metricReplicationLag               = "cnpg_pg_replication_lag"
	metricReplicationInRecovery        = "cnpg_pg_replication_in_recovery"
	metricReplicationWALReceiverUp     = "cnpg_pg_replication_is_wal_receiver_up"
	metricReplicationStreamingReplicas = "cnpg_pg_replication_streaming_replicas"
	metricReplicationReplayDiffBytes   = "cnpg_pg_stat_replication_replay_diff_bytes"
)

// ParseMetrics parses Prometheus text exposition from a CNPG exporter into typed metrics.
func ParseMetrics(r io.Reader) (*InstanceMetrics, error) {
	if r == nil {
		return &InstanceMetrics{}, nil
	}

	parser := expfmt.TextParser{}
	families, err := parser.TextToMetricFamilies(r)
	if err != nil {
		return nil, err
	}

	metrics := &InstanceMetrics{}
	parseConnections(families[metricBackendsTotal], &metrics.Connections)
	metrics.Replication.ReplicationLagSeconds = maxMetricValue(families[metricReplicationLag])
	metrics.Replication.IsReplica = anyMetricValueIsOne(families[metricReplicationInRecovery])
	metrics.Replication.IsWALReceiverUp = anyMetricValueIsOne(families[metricReplicationWALReceiverUp])
	metrics.Replication.StreamingReplicas = int(maxMetricValue(families[metricReplicationStreamingReplicas]))
	metrics.Replication.ReplayLagBytes = maxMetricValue(families[metricReplicationReplayDiffBytes])

	return metrics, nil
}

func parseConnections(family *dto.MetricFamily, connections *ConnectionMetrics) {
	if family == nil || connections == nil {
		return
	}

	total := 0
	for _, metric := range family.GetMetric() {
		value, ok := metricValue(metric)
		if !ok {
			continue
		}
		count := int(value)
		total += count

		switch metricLabel(metric, "state") {
		case "active":
			connections.Active += count
		case "idle":
			connections.Idle += count
		case "idle in transaction":
			connections.IdleInTransaction += count
		}
	}

	connections.Total = total
}

func maxMetricValue(family *dto.MetricFamily) float64 {
	if family == nil {
		return 0
	}

	var max float64
	var seen bool
	for _, metric := range family.GetMetric() {
		value, ok := metricValue(metric)
		if !ok {
			continue
		}
		if !seen || value > max {
			max = value
			seen = true
		}
	}
	if !seen {
		return 0
	}
	return max
}

func anyMetricValueIsOne(family *dto.MetricFamily) bool {
	if family == nil {
		return false
	}

	for _, metric := range family.GetMetric() {
		value, ok := metricValue(metric)
		if ok && value == 1 {
			return true
		}
	}
	return false
}

func metricValue(metric *dto.Metric) (float64, bool) {
	if metric == nil {
		return 0, false
	}
	if gauge := metric.GetGauge(); gauge != nil {
		return gauge.GetValue(), true
	}
	if counter := metric.GetCounter(); counter != nil {
		return counter.GetValue(), true
	}
	if untyped := metric.GetUntyped(); untyped != nil {
		return untyped.GetValue(), true
	}
	return 0, false
}

func metricLabel(metric *dto.Metric, name string) string {
	if metric == nil {
		return ""
	}
	for _, label := range metric.GetLabel() {
		if label.GetName() == name {
			return label.GetValue()
		}
	}
	return ""
}
