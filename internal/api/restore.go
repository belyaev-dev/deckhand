package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

// RestoreCreateOptions captures the sanitized restore options passed from the
// API layer into the injected runtime adapter.
type RestoreCreateOptions struct {
	TargetNamespace string
	TargetName      string
	PITRTargetTime  string
}

// RestoreCreator creates a restored CNPG Cluster for a source cluster + backup.
type RestoreCreator interface {
	CreateCluster(context.Context, *cnpgv1.Cluster, *cnpgv1.Backup, RestoreCreateOptions) (*cnpgv1.Cluster, error)
}

func (h clusterHandlers) listClusterRestoreOptions(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimSpace(chi.URLParam(r, "namespace"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))

	cluster, ok := h.store.GetCluster(namespace, name)
	if !ok {
		writeJSON(w, h.logger, http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("cluster %q in namespace %q not found", name, namespace),
		})
		return
	}

	writeJSON(w, h.logger, http.StatusOK, newClusterRestoreOptionsResponse(cluster, h.store.ListBackupsForCluster(namespace, name)))
}

func (h clusterHandlers) createClusterRestore(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimSpace(chi.URLParam(r, "namespace"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))

	sourceCluster, ok := h.store.GetCluster(namespace, name)
	if !ok {
		writeJSON(w, h.logger, http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("cluster %q in namespace %q not found", name, namespace),
		})
		return
	}

	if h.restores == nil {
		h.logger.Error("restore creator unavailable", "namespace", namespace, "cluster", name)
		writeJSON(w, h.logger, http.StatusServiceUnavailable, ErrorResponse{
			Error: "restore creation is not configured",
		})
		return
	}

	request, err := decodeCreateRestoreRequest(r)
	if err != nil {
		h.logger.Warn("restore create rejected", "namespace", namespace, "cluster", name, "reason", err.Error())
		writeJSON(w, h.logger, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	backup, options, status, err := h.resolveRestoreCreateRequest(sourceCluster, request)
	if err != nil {
		h.logger.Warn("restore create rejected",
			"namespace", namespace,
			"cluster", name,
			"backup", request.BackupName,
			"target_namespace", request.TargetNamespace,
			"target_cluster", request.TargetName,
			"pitr_target_time", request.PITRTargetTime,
			"reason", err.Error(),
		)
		writeJSON(w, h.logger, status, ErrorResponse{Error: err.Error()})
		return
	}

	createdCluster, err := h.restores.CreateCluster(r.Context(), sourceCluster, backup, options)
	if err != nil {
		status = restoreCreateErrorStatus(err)
		h.logger.Warn("restore create failed",
			"namespace", namespace,
			"cluster", name,
			"backup", backup.Name,
			"target_namespace", options.TargetNamespace,
			"target_cluster", options.TargetName,
			"pitr_target_time", options.PITRTargetTime,
			"status", status,
			"error", err,
		)
		writeJSON(w, h.logger, status, ErrorResponse{Error: restoreCreateErrorMessage(options.TargetNamespace, options.TargetName, err)})
		return
	}
	if createdCluster == nil {
		h.logger.Error("restore creator returned nil cluster",
			"namespace", namespace,
			"cluster", name,
			"backup", backup.Name,
			"target_namespace", options.TargetNamespace,
			"target_cluster", options.TargetName,
		)
		writeJSON(w, h.logger, http.StatusInternalServerError, ErrorResponse{Error: "restore creation failed: creator returned no cluster resource"})
		return
	}

	yamlPreview, err := marshalRestorePreview(createdCluster)
	if err != nil {
		h.logger.Error("failed to marshal restore yaml preview",
			"namespace", namespace,
			"cluster", name,
			"backup", backup.Name,
			"target_namespace", options.TargetNamespace,
			"target_cluster", options.TargetName,
			"error", err,
		)
		writeJSON(w, h.logger, http.StatusInternalServerError, ErrorResponse{Error: "restore creation failed: could not serialize cluster manifest"})
		return
	}

	submittedAt := time.Now().UTC()
	response := CreateRestoreResponse{
		SourceCluster: newClusterSummary(sourceCluster),
		TargetCluster: newClusterSummary(createdCluster),
		Backup:        newBackupSummary(backup),
		YAMLPreview:   yamlPreview,
		RestoreStatus: submittedRestoreStatus(submittedAt),
	}

	h.logger.Info("restore create accepted",
		"namespace", namespace,
		"cluster", name,
		"backup", backup.Name,
		"backup_method", backupMethod(backup),
		"target_namespace", response.TargetCluster.Namespace,
		"target_cluster", response.TargetCluster.Name,
		"phase", response.RestoreStatus.Phase,
	)
	writeJSON(w, h.logger, http.StatusCreated, response)
}

func (h clusterHandlers) getRestoreStatus(w http.ResponseWriter, r *http.Request) {
	namespace := strings.TrimSpace(chi.URLParam(r, "namespace"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))

	cluster, ok := h.store.GetCluster(namespace, name)
	if !ok {
		writeJSON(w, h.logger, http.StatusNotFound, ErrorResponse{
			Error: fmt.Sprintf("cluster %q in namespace %q not found", name, namespace),
		})
		return
	}

	writeJSON(w, h.logger, http.StatusOK, RestoreStatusResponse{
		Cluster: newClusterSummary(cluster),
		Status:  newRestoreStatus(cluster),
	})
}

func decodeCreateRestoreRequest(r *http.Request) (CreateRestoreRequest, error) {
	if r == nil || r.Body == nil {
		return CreateRestoreRequest{}, errors.New("restore create request body is required")
	}
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request CreateRestoreRequest
	if err := decoder.Decode(&request); err != nil {
		if errors.Is(err, io.EOF) {
			return CreateRestoreRequest{}, errors.New("restore create request body is required")
		}
		return CreateRestoreRequest{}, fmt.Errorf("invalid restore create request: %w", err)
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CreateRestoreRequest{}, fmt.Errorf("invalid restore create request: body must contain a single JSON object")
	}

	return request, nil
}

func (h clusterHandlers) resolveRestoreCreateRequest(sourceCluster *cnpgv1.Cluster, request CreateRestoreRequest) (*cnpgv1.Backup, RestoreCreateOptions, int, error) {
	if sourceCluster == nil {
		return nil, RestoreCreateOptions{}, http.StatusNotFound, errors.New("source cluster is required")
	}

	backupName := strings.TrimSpace(request.BackupName)
	if backupName == "" {
		return nil, RestoreCreateOptions{}, http.StatusBadRequest, errors.New("backupName is required")
	}
	backup, ok := h.store.GetBackup(sourceCluster.Namespace, backupName)
	if !ok || backup.Spec.Cluster.Name != sourceCluster.Name {
		return nil, RestoreCreateOptions{}, http.StatusBadRequest, fmt.Errorf("backup %q was not found for cluster %q in namespace %q", backupName, sourceCluster.Name, sourceCluster.Namespace)
	}
	if backup.Status.Phase != cnpgv1.BackupPhaseCompleted {
		return nil, RestoreCreateOptions{}, http.StatusConflict, fmt.Errorf("backup %q is not completed and cannot be restored", backupName)
	}

	method := backupMethod(backup)
	if method != cnpgv1.BackupMethodBarmanObjectStore && method != cnpgv1.BackupMethodVolumeSnapshot {
		return nil, RestoreCreateOptions{}, http.StatusBadRequest, fmt.Errorf("backup %q uses unsupported restore method %q", backupName, method)
	}
	if method == cnpgv1.BackupMethodBarmanObjectStore && !canRestoreFromBackupCatalog(sourceCluster, backup) {
		return nil, RestoreCreateOptions{}, http.StatusConflict, fmt.Errorf("backup %q does not expose object-store restore configuration", backupName)
	}
	if method == cnpgv1.BackupMethodBarmanObjectStore && strings.TrimSpace(backup.Status.BackupID) == "" {
		return nil, RestoreCreateOptions{}, http.StatusConflict, fmt.Errorf("backup %q does not expose a backup ID for restore", backupName)
	}

	options, status, err := resolveRestoreCreateOptions(sourceCluster, backup, request)
	if err != nil {
		return nil, RestoreCreateOptions{}, status, err
	}

	if _, exists := h.store.GetCluster(options.TargetNamespace, options.TargetName); exists {
		return nil, RestoreCreateOptions{}, http.StatusConflict, fmt.Errorf("cluster %q in namespace %q already exists", options.TargetName, options.TargetNamespace)
	}

	return backup, options, http.StatusOK, nil
}

func resolveRestoreCreateOptions(sourceCluster *cnpgv1.Cluster, backup *cnpgv1.Backup, request CreateRestoreRequest) (RestoreCreateOptions, int, error) {
	if sourceCluster == nil {
		return RestoreCreateOptions{}, http.StatusNotFound, errors.New("source cluster is required")
	}
	if backup == nil {
		return RestoreCreateOptions{}, http.StatusBadRequest, errors.New("backup is required")
	}

	targetNamespace := strings.TrimSpace(request.TargetNamespace)
	if targetNamespace == "" {
		return RestoreCreateOptions{}, http.StatusBadRequest, errors.New("targetNamespace is required")
	}
	if errs := validation.IsDNS1123Subdomain(targetNamespace); len(errs) > 0 {
		return RestoreCreateOptions{}, http.StatusBadRequest, fmt.Errorf("targetNamespace %q is invalid: %s", targetNamespace, strings.Join(errs, "; "))
	}

	targetName := strings.TrimSpace(request.TargetName)
	if targetName == "" {
		return RestoreCreateOptions{}, http.StatusBadRequest, errors.New("targetName is required")
	}
	if errs := validation.IsDNS1123Subdomain(targetName); len(errs) > 0 {
		return RestoreCreateOptions{}, http.StatusBadRequest, fmt.Errorf("targetName %q is invalid: %s", targetName, strings.Join(errs, "; "))
	}
	if targetNamespace == sourceCluster.Namespace && targetName == sourceCluster.Name {
		return RestoreCreateOptions{}, http.StatusConflict, errors.New("restore target must create a new cluster instead of overwriting the source cluster")
	}

	pitrTargetTime := strings.TrimSpace(request.PITRTargetTime)
	if pitrTargetTime != "" {
		if backupMethod(backup) != cnpgv1.BackupMethodBarmanObjectStore {
			return RestoreCreateOptions{}, http.StatusBadRequest, fmt.Errorf("backup %q does not support PITR target time", backup.Name)
		}

		parsed, err := time.Parse(time.RFC3339, pitrTargetTime)
		if err != nil {
			return RestoreCreateOptions{}, http.StatusBadRequest, fmt.Errorf("pitrTargetTime must be RFC3339: %w", err)
		}
		parsed = parsed.UTC()

		start := firstRecoverabilityPoint(sourceCluster)
		end := lastSuccessfulBackup(sourceCluster)
		if start == nil || end == nil {
			return RestoreCreateOptions{}, http.StatusConflict, fmt.Errorf("cluster %q in namespace %q does not advertise a recoverability window for PITR", sourceCluster.Name, sourceCluster.Namespace)
		}
		if parsed.Before(*start) || parsed.After(*end) {
			return RestoreCreateOptions{}, http.StatusBadRequest, fmt.Errorf("pitrTargetTime %q is outside the recoverability window %s - %s", parsed.Format(time.RFC3339), start.Format(time.RFC3339), end.Format(time.RFC3339))
		}
		pitrTargetTime = parsed.Format(time.RFC3339)
	}

	return RestoreCreateOptions{
		TargetNamespace: targetNamespace,
		TargetName:      targetName,
		PITRTargetTime:  pitrTargetTime,
	}, http.StatusOK, nil
}

func canRestoreFromBackupCatalog(cluster *cnpgv1.Cluster, backup *cnpgv1.Backup) bool {
	if cluster != nil && cluster.Spec.Backup != nil && cluster.Spec.Backup.BarmanObjectStore != nil {
		return true
	}
	if backup == nil {
		return false
	}
	return strings.TrimSpace(backup.Status.DestinationPath) != ""
}

func restoreCreateErrorStatus(err error) int {
	switch {
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		return http.StatusConflict
	case apierrors.IsBadRequest(err), apierrors.IsInvalid(err):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func restoreCreateErrorMessage(namespace, name string, err error) string {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "restore creation failed"
	}
	return fmt.Sprintf("create restore cluster %q in namespace %q: %s", name, namespace, message)
}

func marshalRestorePreview(cluster *cnpgv1.Cluster) (string, error) {
	body, err := yaml.Marshal(cluster)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func submittedRestoreStatus(submittedAt time.Time) RestoreStatus {
	return RestoreStatus{
		Phase:       restorePhaseBootstrapping,
		PhaseReason: "create accepted",
		Message:     "restore cluster resource created",
		Timestamps: RestorePhaseTimestamps{
			BootstrappingStartedAt: timePtr(submittedAt),
			LastTransitionAt:       timePtr(submittedAt),
		},
	}
}

func newRestoreStatus(cluster *cnpgv1.Cluster) RestoreStatus {
	status := RestoreStatus{
		Phase:       restorePhaseBootstrapping,
		PhaseReason: clusterPhaseReason(cluster),
		Message:     redactDiagnostic(firstNonEmpty(readyConditionMessage(cluster), clusterPhaseReason(cluster), clusterPhase(cluster))),
		Timestamps: RestorePhaseTimestamps{
			BootstrappingStartedAt: metaTime(cluster.CreationTimestamp),
		},
	}

	recoveringStartedAt := restoreRecoveringStartedAt(cluster)
	readyAt := restoreReadyAt(cluster)
	failedAt := restoreFailedAt(cluster)

	switch {
	case failedAt != nil || isRestoreFailure(cluster):
		status.Phase = restorePhaseFailed
		status.Error = redactDiagnostic(firstNonEmpty(readyConditionMessage(cluster), clusterPhaseReason(cluster), clusterPhase(cluster)))
		status.Message = status.Error
		status.Timestamps.FailedAt = failedAt
		status.Timestamps.RecoveringStartedAt = recoveringStartedAt
	case readyAt != nil || isRestoreReady(cluster):
		status.Phase = restorePhaseReady
		status.Timestamps.RecoveringStartedAt = recoveringStartedAt
		status.Timestamps.ReadyAt = readyAt
	case isRestoreRecovering(cluster):
		status.Phase = restorePhaseRecovering
		status.Timestamps.RecoveringStartedAt = recoveringStartedAt
	}

	status.Timestamps.LastTransitionAt = restoreLastTransitionAt(status.Timestamps)
	return status
}

func clusterPhase(cluster *cnpgv1.Cluster) string {
	if cluster == nil {
		return ""
	}
	return cluster.Status.Phase
}

func clusterPhaseReason(cluster *cnpgv1.Cluster) string {
	if cluster == nil {
		return ""
	}
	return redactDiagnostic(cluster.Status.PhaseReason)
}

func readyCondition(cluster *cnpgv1.Cluster) *metav1.Condition {
	if cluster == nil {
		return nil
	}
	for idx := range cluster.Status.Conditions {
		condition := &cluster.Status.Conditions[idx]
		if condition.Type == string(cnpgv1.ConditionClusterReady) {
			return condition
		}
	}
	return nil
}

func readyConditionMessage(cluster *cnpgv1.Cluster) string {
	condition := readyCondition(cluster)
	if condition == nil {
		return ""
	}
	return condition.Message
}

func isRestoreReady(cluster *cnpgv1.Cluster) bool {
	if cluster == nil {
		return false
	}
	if condition := readyCondition(cluster); condition != nil && condition.Status == metav1.ConditionTrue {
		return true
	}
	combined := strings.ToLower(strings.Join([]string{cluster.Status.Phase, cluster.Status.PhaseReason}, " "))
	return strings.Contains(combined, "ready") || (cluster.Spec.Instances > 0 && cluster.Status.ReadyInstances >= cluster.Spec.Instances)
}

func isRestoreFailure(cluster *cnpgv1.Cluster) bool {
	if cluster == nil {
		return false
	}
	combined := strings.ToLower(strings.Join([]string{cluster.Status.Phase, cluster.Status.PhaseReason, readyConditionMessage(cluster)}, " "))
	for _, marker := range []string{"fail", "error", "degrad", "invalid"} {
		if strings.Contains(combined, marker) {
			return true
		}
	}
	return false
}

func isRestoreRecovering(cluster *cnpgv1.Cluster) bool {
	if cluster == nil || isRestoreReady(cluster) || isRestoreFailure(cluster) {
		return false
	}
	combined := strings.ToLower(strings.Join([]string{cluster.Status.Phase, cluster.Status.PhaseReason}, " "))
	if strings.Contains(combined, "recover") || strings.Contains(combined, "restor") {
		return true
	}
	return cluster.Status.CurrentPrimary != "" || cluster.Status.ReadyInstances > 0
}

func restoreRecoveringStartedAt(cluster *cnpgv1.Cluster) *time.Time {
	if cluster == nil || !isRestoreRecovering(cluster) && !isRestoreReady(cluster) && !isRestoreFailure(cluster) {
		return nil
	}
	for _, raw := range []string{cluster.Status.CurrentPrimaryTimestamp, cluster.Status.TargetPrimaryTimestamp} {
		if parsed := parseTimestamp(raw); parsed != nil {
			return parsed
		}
	}
	if condition := readyCondition(cluster); condition != nil && !condition.LastTransitionTime.IsZero() {
		return metaTime(condition.LastTransitionTime)
	}
	return metaTime(cluster.CreationTimestamp)
}

func restoreReadyAt(cluster *cnpgv1.Cluster) *time.Time {
	if cluster == nil || !isRestoreReady(cluster) {
		return nil
	}
	if condition := readyCondition(cluster); condition != nil && condition.Status == metav1.ConditionTrue && !condition.LastTransitionTime.IsZero() {
		return metaTime(condition.LastTransitionTime)
	}
	for _, raw := range []string{cluster.Status.CurrentPrimaryTimestamp, cluster.Status.TargetPrimaryTimestamp} {
		if parsed := parseTimestamp(raw); parsed != nil {
			return parsed
		}
	}
	return metaTime(cluster.CreationTimestamp)
}

func restoreFailedAt(cluster *cnpgv1.Cluster) *time.Time {
	if cluster == nil || !isRestoreFailure(cluster) {
		return nil
	}
	if condition := readyCondition(cluster); condition != nil && condition.Status == metav1.ConditionFalse && !condition.LastTransitionTime.IsZero() {
		return metaTime(condition.LastTransitionTime)
	}
	if parsed := parseTimestamp(cluster.Status.CurrentPrimaryFailingSinceTimestamp); parsed != nil {
		return parsed
	}
	return metaTime(cluster.CreationTimestamp)
}

func restoreLastTransitionAt(timestamps RestorePhaseTimestamps) *time.Time {
	var latest *time.Time
	for _, candidate := range []*time.Time{
		timestamps.BootstrappingStartedAt,
		timestamps.RecoveringStartedAt,
		timestamps.ReadyAt,
		timestamps.FailedAt,
	} {
		if candidate == nil {
			continue
		}
		if latest == nil || candidate.After(*latest) {
			copy := candidate.UTC()
			latest = &copy
		}
	}
	return latest
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func backupMethod(backup *cnpgv1.Backup) cnpgv1.BackupMethod {
	if backup == nil {
		return ""
	}
	if backup.Status.Method != "" {
		return backup.Status.Method
	}
	return backup.Spec.Method
}
