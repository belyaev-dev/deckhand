package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/deckhand-for-cnpg/deckhand/internal/metrics"
	"github.com/go-chi/chi/v5"
)

type metricsReader interface {
	GetClusterMetrics(namespace, name string) (*metrics.ClusterMetrics, bool)
}

func (h clusterHandlers) getClusterMetrics(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimSpace(chi.URLParam(r, "namespace"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))

	cluster, ok := h.store.GetCluster(namespace, name)
	if !ok {
		writeJSON(w, h.logger, http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("cluster %q in namespace %q not found", name, namespace),
		})
		return
	}

	var snapshot *metrics.ClusterMetrics
	if h.metrics != nil {
		if cached, ok := h.metrics.GetClusterMetrics(namespace, name); ok {
			snapshot = cached
		}
	}

	writeJSON(w, h.logger, http.StatusOK, newClusterMetricsResponse(cluster, snapshot))
}
