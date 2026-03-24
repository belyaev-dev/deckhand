package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/deckhand-for-cnpg/deckhand/internal/metrics"
	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	"github.com/go-chi/chi/v5"
)

type clusterHandlers struct {
	logger   *slog.Logger
	store    *store.Store
	metrics  metricsReader
	backups  BackupCreator
	restores RestoreCreator
}

func newClusterHandlers(logger *slog.Logger, st *store.Store, reader metricsReader, backupCreator BackupCreator, restoreCreator RestoreCreator) clusterHandlers {
	if st == nil {
		st = store.New()
	}

	return clusterHandlers{
		logger:   ensureLogger(logger),
		store:    st,
		metrics:  reader,
		backups:  backupCreator,
		restores: restoreCreator,
	}
}

func (h clusterHandlers) listClusters(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	clusters := h.store.ListClusters(namespace)

	namespaceCounts := make(map[string]int, len(clusters))
	response := ClusterListResponse{
		Namespaces: make([]ClusterNamespaceSummary, 0, len(clusters)),
		Items:      make([]ClusterOverviewSummary, 0, len(clusters)),
	}
	for _, cluster := range clusters {
		namespaceCounts[cluster.Namespace]++

		var snapshot *metrics.ClusterMetrics
		if h.metrics != nil {
			if cached, ok := h.metrics.GetClusterMetrics(cluster.Namespace, cluster.Name); ok {
				snapshot = cached
			}
		}

		response.Items = append(response.Items, newClusterOverviewSummary(cluster, snapshot))
	}

	for namespace, clusterCount := range namespaceCounts {
		response.Namespaces = append(response.Namespaces, ClusterNamespaceSummary{
			Name:         namespace,
			ClusterCount: clusterCount,
		})
	}
	sort.Slice(response.Namespaces, func(i, j int) bool {
		return response.Namespaces[i].Name < response.Namespaces[j].Name
	})

	writeJSON(w, h.logger, http.StatusOK, response)
}

func (h clusterHandlers) getCluster(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimSpace(chi.URLParam(r, "namespace"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))

	cluster, ok := h.store.GetCluster(namespace, name)
	if !ok {
		writeJSON(w, h.logger, http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("cluster %q in namespace %q not found", name, namespace),
		})
		return
	}

	backups := h.store.ListBackupsForCluster(namespace, name)
	scheduledBackups := h.store.ListScheduledBackupsForCluster(namespace, name)

	response := ClusterDetailResponse{
		Cluster:          newClusterSummary(cluster),
		Backups:          make([]BackupSummary, 0, len(backups)),
		ScheduledBackups: make([]ScheduledBackupSummary, 0, len(scheduledBackups)),
	}
	for _, backup := range backups {
		response.Backups = append(response.Backups, newBackupSummary(backup))
	}
	for _, scheduledBackup := range scheduledBackups {
		response.ScheduledBackups = append(response.ScheduledBackups, newScheduledBackupSummary(scheduledBackup))
	}

	writeJSON(w, h.logger, http.StatusOK, response)
}

func writeJSON(w http.ResponseWriter, logger *slog.Logger, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		logger.Error("failed to marshal API response", "status", status, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
