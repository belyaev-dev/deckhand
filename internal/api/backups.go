package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// BackupCreateOptions captures the sanitized backup creation options passed from
// the API layer into the injected runtime adapter.
type BackupCreateOptions struct {
	Method cnpgv1.BackupMethod
	Target cnpgv1.BackupTarget
}

// BackupCreator creates an on-demand CNPG Backup for a specific cluster.
type BackupCreator interface {
	CreateBackup(context.Context, *cnpgv1.Cluster, BackupCreateOptions) (*cnpgv1.Backup, error)
}

func (h clusterHandlers) listClusterBackups(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimSpace(chi.URLParam(r, "namespace"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))

	cluster, ok := h.store.GetCluster(namespace, name)
	if !ok {
		writeJSON(w, h.logger, http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("cluster %q in namespace %q not found", name, namespace),
		})
		return
	}

	writeJSON(w, h.logger, http.StatusOK, newClusterBackupsResponse(cluster, h.store.ListBackupsForCluster(namespace, name), h.store.ListScheduledBackupsForCluster(namespace, name)))
}

func (h clusterHandlers) createClusterBackup(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimSpace(chi.URLParam(r, "namespace"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))

	cluster, ok := h.store.GetCluster(namespace, name)
	if !ok {
		writeJSON(w, h.logger, http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("cluster %q in namespace %q not found", name, namespace),
		})
		return
	}

	if h.backups == nil {
		h.logger.Error("backup creator unavailable", "namespace", namespace, "cluster", name)
		writeJSON(w, h.logger, http.StatusServiceUnavailable, ErrorResponse{
			Error: "backup creation is not configured",
		})
		return
	}

	request, err := decodeCreateBackupRequest(r)
	if err != nil {
		h.logger.Warn("backup create rejected", "namespace", namespace, "cluster", name, "reason", err.Error())
		writeJSON(w, h.logger, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	options, status, err := resolveBackupCreateOptions(cluster, request)
	if err != nil {
		h.logger.Warn("backup create rejected",
			"namespace", namespace,
			"cluster", name,
			"method", request.Method,
			"target", request.Target,
			"reason", err.Error(),
		)
		writeJSON(w, h.logger, status, ErrorResponse{Error: err.Error()})
		return
	}

	created, err := h.backups.CreateBackup(r.Context(), cluster, options)
	if err != nil {
		status = backupCreateErrorStatus(err)
		h.logger.Warn("backup create failed",
			"namespace", namespace,
			"cluster", name,
			"method", options.Method,
			"target", options.Target,
			"status", status,
			"error", err,
		)
		writeJSON(w, h.logger, status, ErrorResponse{Error: backupCreateErrorMessage(namespace, name, err)})
		return
	}
	if created == nil {
		h.logger.Error("backup creator returned nil backup", "namespace", namespace, "cluster", name)
		writeJSON(w, h.logger, http.StatusInternalServerError, ErrorResponse{Error: "backup creation failed: creator returned no backup resource"})
		return
	}

	summary := newBackupSummary(created)
	h.logger.Info("backup create accepted",
		"namespace", namespace,
		"cluster", name,
		"backup", summary.Name,
		"method", summary.Method,
		"target", summary.Target,
		"phase", summary.Phase,
	)
	writeJSON(w, h.logger, http.StatusCreated, CreateBackupResponse{Backup: summary})
}

func newClusterBackupsResponse(cluster *cnpgv1.Cluster, backups []*cnpgv1.Backup, scheduledBackups []*cnpgv1.ScheduledBackup) ClusterBackupsResponse {
	response := ClusterBackupsResponse{
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
	return response
}

func decodeCreateBackupRequest(r *http.Request) (CreateBackupRequest, error) {
	if r == nil || r.Body == nil {
		return CreateBackupRequest{}, nil
	}
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request CreateBackupRequest
	if err := decoder.Decode(&request); err != nil {
		if errors.Is(err, io.EOF) {
			return CreateBackupRequest{}, nil
		}
		return CreateBackupRequest{}, fmt.Errorf("invalid backup create request: %w", err)
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CreateBackupRequest{}, fmt.Errorf("invalid backup create request: body must contain a single JSON object")
	}

	return request, nil
}

func resolveBackupCreateOptions(cluster *cnpgv1.Cluster, request CreateBackupRequest) (BackupCreateOptions, int, error) {
	if cluster == nil {
		return BackupCreateOptions{}, http.StatusNotFound, errors.New("cluster is required")
	}

	method := request.Method
	if method == "" {
		var ok bool
		method, ok = defaultBackupMethod(cluster)
		if !ok {
			return BackupCreateOptions{}, http.StatusConflict, fmt.Errorf("cluster %q in namespace %q is not configured for backups", cluster.Name, cluster.Namespace)
		}
	}

	if !isSupportedBackupMethod(method) {
		return BackupCreateOptions{}, http.StatusBadRequest, fmt.Errorf("backup method %q is not supported", method)
	}
	if !clusterSupportsBackupMethod(cluster, method) {
		return BackupCreateOptions{}, http.StatusConflict, fmt.Errorf("cluster %q in namespace %q is not configured for %q backups", cluster.Name, cluster.Namespace, method)
	}

	target := request.Target
	if target == "" {
		target = defaultBackupTarget(cluster)
	}
	if !isSupportedBackupTarget(target) {
		return BackupCreateOptions{}, http.StatusBadRequest, fmt.Errorf("backup target %q is not supported", target)
	}

	return BackupCreateOptions{Method: method, Target: target}, http.StatusOK, nil
}

func defaultBackupMethod(cluster *cnpgv1.Cluster) (cnpgv1.BackupMethod, bool) {
	if cluster == nil || cluster.Spec.Backup == nil {
		return "", false
	}
	if cluster.Spec.Backup.IsBarmanBackupConfigured() {
		return cnpgv1.BackupMethodBarmanObjectStore, true
	}
	if cluster.Spec.Backup.VolumeSnapshot != nil {
		return cnpgv1.BackupMethodVolumeSnapshot, true
	}
	return "", false
}

func defaultBackupTarget(cluster *cnpgv1.Cluster) cnpgv1.BackupTarget {
	if cluster != nil && cluster.Spec.Backup != nil && cluster.Spec.Backup.Target != "" {
		return cluster.Spec.Backup.Target
	}
	return cnpgv1.DefaultBackupTarget
}

func clusterSupportsBackupMethod(cluster *cnpgv1.Cluster, method cnpgv1.BackupMethod) bool {
	if cluster == nil || cluster.Spec.Backup == nil {
		return false
	}

	switch method {
	case cnpgv1.BackupMethodBarmanObjectStore:
		return cluster.Spec.Backup.IsBarmanBackupConfigured()
	case cnpgv1.BackupMethodVolumeSnapshot:
		return cluster.Spec.Backup.VolumeSnapshot != nil
	default:
		return false
	}
}

func isSupportedBackupMethod(method cnpgv1.BackupMethod) bool {
	switch method {
	case cnpgv1.BackupMethodBarmanObjectStore, cnpgv1.BackupMethodVolumeSnapshot:
		return true
	default:
		return false
	}
}

func isSupportedBackupTarget(target cnpgv1.BackupTarget) bool {
	switch target {
	case cnpgv1.BackupTargetPrimary, cnpgv1.BackupTargetStandby:
		return true
	default:
		return false
	}
}

func backupCreateErrorStatus(err error) int {
	switch {
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		return http.StatusConflict
	case apierrors.IsBadRequest(err), apierrors.IsInvalid(err):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func backupCreateErrorMessage(namespace, name string, err error) string {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "backup creation failed"
	}
	return fmt.Sprintf("create backup for cluster %q in namespace %q: %s", name, namespace, message)
}
