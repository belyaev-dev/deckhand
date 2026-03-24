package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestListRestoreOptions(t *testing.T) {
	st := store.New()
	if err := st.UpsertCluster(apiTestClusterWithBackupConfig("team-a", "alpha")); err != nil {
		t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
	}
	if err := st.UpsertBackup(apiRestoreTestBackup("team-a", "alpha-backup-20260324", "alpha")); err != nil {
		t.Fatalf("UpsertBackup(team-a/alpha-backup-20260324) error: %v", err)
	}
	if err := st.UpsertBackup(apiTestBackup("team-a", "other-backup", "other")); err != nil {
		t.Fatalf("UpsertBackup(team-a/other-backup) error: %v", err)
	}

	router := NewRouter(ServerDeps{Store: st})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/clusters/team-a/alpha/restore", nil)

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/clusters/team-a/alpha/restore status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}

	var response ClusterRestoreOptionsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(restore options response): %v", err)
	}
	if response.Cluster.Namespace != "team-a" || response.Cluster.Name != "alpha" {
		t.Fatalf("response.Cluster = %#v, want namespace=%q name=%q", response.Cluster, "team-a", "alpha")
	}
	if got := len(response.Backups); got != 1 {
		t.Fatalf("len(response.Backups) = %d, want 1", got)
	}
	if got, want := response.Backups[0].Name, "alpha-backup-20260324"; got != want {
		t.Fatalf("response.Backups[0].Name = %q, want %q", got, want)
	}
	if response.Recoverability.Start == nil || response.Recoverability.End == nil {
		t.Fatalf("response.Recoverability = %#v, want populated window", response.Recoverability)
	}
	if got := response.SupportedPhases; len(got) != 4 || got[0] != restorePhaseBootstrapping || got[3] != restorePhaseFailed {
		t.Fatalf("response.SupportedPhases = %#v, want restore phase list", got)
	}
}

func TestCreateRestore(t *testing.T) {
	t.Run("creates a restore cluster, returns yaml preview, and exposes initial progress state", func(t *testing.T) {
		st := store.New()
		source := apiTestClusterWithBackupConfig("team-a", "alpha")
		if err := st.UpsertCluster(source); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}
		backup := apiRestoreTestBackup("team-a", "alpha-backup-20260324", "alpha")
		if err := st.UpsertBackup(backup); err != nil {
			t.Fatalf("UpsertBackup(team-a/alpha-backup-20260324) error: %v", err)
		}

		creator := &fakeRestoreCreator{
			create: func(_ context.Context, sourceCluster *cnpgv1.Cluster, selectedBackup *cnpgv1.Backup, options RestoreCreateOptions) (*cnpgv1.Cluster, error) {
				return &cnpgv1.Cluster{
					TypeMeta:   metav1.TypeMeta{APIVersion: cnpgv1.SchemeGroupVersion.String(), Kind: "Cluster"},
					ObjectMeta: metav1.ObjectMeta{Namespace: options.TargetNamespace, Name: options.TargetName},
					Spec: cnpgv1.ClusterSpec{
						Instances: sourceCluster.Spec.Instances,
						Bootstrap: &cnpgv1.BootstrapConfiguration{Recovery: &cnpgv1.BootstrapRecovery{
							Source: sourceCluster.Name,
							RecoveryTarget: &cnpgv1.RecoveryTarget{
								BackupID:   selectedBackup.Status.BackupID,
								TargetTime: options.PITRTargetTime,
							},
						}},
					},
				}, nil
			},
		}

		router := NewRouter(ServerDeps{Store: st, RestoreCreator: creator})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/clusters/team-a/alpha/restore", strings.NewReader(`{"backupName":"alpha-backup-20260324","targetNamespace":"team-b","targetName":"alpha-restore","pitrTargetTime":"2026-03-24T11:15:00Z"}`))
		request.Header.Set("Content-Type", "application/json")

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusCreated {
			t.Fatalf("POST /api/clusters/team-a/alpha/restore status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
		}
		if creator.calls != 1 {
			t.Fatalf("creator.calls = %d, want 1", creator.calls)
		}
		if got, want := creator.lastSourceCluster.Name, "alpha"; got != want {
			t.Fatalf("creator.lastSourceCluster.Name = %q, want %q", got, want)
		}
		if got, want := creator.lastBackup.Name, "alpha-backup-20260324"; got != want {
			t.Fatalf("creator.lastBackup.Name = %q, want %q", got, want)
		}
		if got, want := creator.lastOptions.TargetNamespace, "team-b"; got != want {
			t.Fatalf("creator.lastOptions.TargetNamespace = %q, want %q", got, want)
		}
		if got, want := creator.lastOptions.TargetName, "alpha-restore"; got != want {
			t.Fatalf("creator.lastOptions.TargetName = %q, want %q", got, want)
		}
		if got, want := creator.lastOptions.PITRTargetTime, "2026-03-24T11:15:00Z"; got != want {
			t.Fatalf("creator.lastOptions.PITRTargetTime = %q, want %q", got, want)
		}

		var response CreateRestoreResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(create restore response): %v", err)
		}
		if got, want := response.SourceCluster.Name, "alpha"; got != want {
			t.Fatalf("response.SourceCluster.Name = %q, want %q", got, want)
		}
		if got, want := response.TargetCluster.Namespace, "team-b"; got != want {
			t.Fatalf("response.TargetCluster.Namespace = %q, want %q", got, want)
		}
		if got, want := response.TargetCluster.Name, "alpha-restore"; got != want {
			t.Fatalf("response.TargetCluster.Name = %q, want %q", got, want)
		}
		if got, want := response.Backup.Name, "alpha-backup-20260324"; got != want {
			t.Fatalf("response.Backup.Name = %q, want %q", got, want)
		}
		if got, want := response.RestoreStatus.Phase, restorePhaseBootstrapping; got != want {
			t.Fatalf("response.RestoreStatus.Phase = %q, want %q", got, want)
		}
		if response.RestoreStatus.Timestamps.BootstrappingStartedAt == nil {
			t.Fatalf("response.RestoreStatus.Timestamps = %#v, want bootstrapping timestamp", response.RestoreStatus.Timestamps)
		}
		for _, fragment := range []string{"kind: Cluster", "name: alpha-restore", "source: alpha", "targetTime: \"2026-03-24T11:15:00Z\""} {
			if !bytes.Contains([]byte(response.YAMLPreview), []byte(fragment)) {
				t.Fatalf("response.YAMLPreview missing %q: %s", fragment, response.YAMLPreview)
			}
		}
	})

	t.Run("rejects missing or unknown backups before calling the creator", func(t *testing.T) {
		st := store.New()
		if err := st.UpsertCluster(apiTestClusterWithBackupConfig("team-a", "alpha")); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}
		creator := &fakeRestoreCreator{}
		router := NewRouter(ServerDeps{Store: st, RestoreCreator: creator})

		testCases := []struct {
			name       string
			body       string
			wantStatus int
			wantError  string
		}{
			{
				name:       "missing backupName",
				body:       `{"targetNamespace":"team-b","targetName":"alpha-restore"}`,
				wantStatus: http.StatusBadRequest,
				wantError:  "backupName is required",
			},
			{
				name:       "unknown backup",
				body:       `{"backupName":"missing","targetNamespace":"team-b","targetName":"alpha-restore"}`,
				wantStatus: http.StatusBadRequest,
				wantError:  `backup "missing" was not found for cluster "alpha" in namespace "team-a"`,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/api/clusters/team-a/alpha/restore", strings.NewReader(tc.body))
				request.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(recorder, request)

				if recorder.Code != tc.wantStatus {
					t.Fatalf("POST /api/clusters/team-a/alpha/restore status = %d, want %d; body=%s", recorder.Code, tc.wantStatus, recorder.Body.String())
				}
				var response ErrorResponse
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatalf("json.Unmarshal(error response): %v", err)
				}
				if got, want := response.Error, tc.wantError; got != want {
					t.Fatalf("response.Error = %q, want %q", got, want)
				}
			})
		}
		if creator.calls != 0 {
			t.Fatalf("creator.calls = %d, want 0", creator.calls)
		}
	})

	t.Run("rejects invalid target identity and PITR timestamps outside the advertised window", func(t *testing.T) {
		st := store.New()
		if err := st.UpsertCluster(apiTestClusterWithBackupConfig("team-a", "alpha")); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}
		if err := st.UpsertBackup(apiRestoreTestBackup("team-a", "alpha-backup-20260324", "alpha")); err != nil {
			t.Fatalf("UpsertBackup(team-a/alpha-backup-20260324) error: %v", err)
		}
		creator := &fakeRestoreCreator{}
		router := NewRouter(ServerDeps{Store: st, RestoreCreator: creator})

		testCases := []struct {
			name          string
			body          string
			wantStatus    int
			wantError     string
			matchContains bool
		}{
			{
				name:          "invalid target name",
				body:          `{"backupName":"alpha-backup-20260324","targetNamespace":"team-b","targetName":"Alpha_Restore"}`,
				wantStatus:    http.StatusBadRequest,
				wantError:     `targetName "Alpha_Restore" is invalid: a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters`,
				matchContains: true,
			},
			{
				name:       "pitr outside recoverability window",
				body:       `{"backupName":"alpha-backup-20260324","targetNamespace":"team-b","targetName":"alpha-restore","pitrTargetTime":"2026-03-24T09:59:00Z"}`,
				wantStatus: http.StatusBadRequest,
				wantError:  `pitrTargetTime "2026-03-24T09:59:00Z" is outside the recoverability window 2026-03-24T10:00:00Z - 2026-03-24T11:30:00Z`,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/api/clusters/team-a/alpha/restore", strings.NewReader(tc.body))
				request.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(recorder, request)

				if recorder.Code != tc.wantStatus {
					t.Fatalf("POST /api/clusters/team-a/alpha/restore status = %d, want %d; body=%s", recorder.Code, tc.wantStatus, recorder.Body.String())
				}
				var response ErrorResponse
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatalf("json.Unmarshal(error response): %v", err)
				}
				if tc.matchContains {
					if !strings.Contains(response.Error, tc.wantError) {
						t.Fatalf("response.Error = %q, want substring %q", response.Error, tc.wantError)
					}
				} else if got, want := response.Error, tc.wantError; got != want {
					t.Fatalf("response.Error = %q, want %q", got, want)
				}
			})
		}
		if creator.calls != 0 {
			t.Fatalf("creator.calls = %d, want 0", creator.calls)
		}
	})

	t.Run("returns 409 on target conflicts and creator conflicts", func(t *testing.T) {
		st := store.New()
		if err := st.UpsertCluster(apiTestClusterWithBackupConfig("team-a", "alpha")); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}
		if err := st.UpsertBackup(apiRestoreTestBackup("team-a", "alpha-backup-20260324", "alpha")); err != nil {
			t.Fatalf("UpsertBackup(team-a/alpha-backup-20260324) error: %v", err)
		}
		if err := st.UpsertCluster(apiTestCluster("team-b", "alpha-restore")); err != nil {
			t.Fatalf("UpsertCluster(team-b/alpha-restore) error: %v", err)
		}
		creator := &fakeRestoreCreator{}
		router := NewRouter(ServerDeps{Store: st, RestoreCreator: creator})

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/clusters/team-a/alpha/restore", strings.NewReader(`{"backupName":"alpha-backup-20260324","targetNamespace":"team-b","targetName":"alpha-restore"}`))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusConflict {
			t.Fatalf("POST restore target conflict status = %d, want %d; body=%s", recorder.Code, http.StatusConflict, recorder.Body.String())
		}
		var response ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(error response): %v", err)
		}
		if got, want := response.Error, `cluster "alpha-restore" in namespace "team-b" already exists`; got != want {
			t.Fatalf("response.Error = %q, want %q", got, want)
		}
		if creator.calls != 0 {
			t.Fatalf("creator.calls = %d, want 0", creator.calls)
		}

		conflictStore := store.New()
		if err := conflictStore.UpsertCluster(apiTestClusterWithBackupConfig("team-a", "alpha")); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}
		if err := conflictStore.UpsertBackup(apiRestoreTestBackup("team-a", "alpha-backup-20260324", "alpha")); err != nil {
			t.Fatalf("UpsertBackup(team-a/alpha-backup-20260324) error: %v", err)
		}
		conflictCreator := &fakeRestoreCreator{
			create: func(_ context.Context, _ *cnpgv1.Cluster, _ *cnpgv1.Backup, _ RestoreCreateOptions) (*cnpgv1.Cluster, error) {
				return nil, apierrors.NewConflict(schema.GroupResource{Group: cnpgv1.SchemeGroupVersion.Group, Resource: "clusters"}, "alpha-restore", errors.New("cluster already exists"))
			},
		}
		conflictRouter := NewRouter(ServerDeps{Store: conflictStore, RestoreCreator: conflictCreator})
		conflictRecorder := httptest.NewRecorder()
		conflictRequest := httptest.NewRequest(http.MethodPost, "/api/clusters/team-a/alpha/restore", strings.NewReader(`{"backupName":"alpha-backup-20260324","targetNamespace":"team-b","targetName":"alpha-restore"}`))
		conflictRequest.Header.Set("Content-Type", "application/json")
		conflictRouter.ServeHTTP(conflictRecorder, conflictRequest)

		if conflictRecorder.Code != http.StatusConflict {
			t.Fatalf("POST restore creator conflict status = %d, want %d; body=%s", conflictRecorder.Code, http.StatusConflict, conflictRecorder.Body.String())
		}
		var conflictResponse ErrorResponse
		if err := json.Unmarshal(conflictRecorder.Body.Bytes(), &conflictResponse); err != nil {
			t.Fatalf("json.Unmarshal(error response): %v", err)
		}
		if !strings.Contains(conflictResponse.Error, `create restore cluster "alpha-restore" in namespace "team-b":`) {
			t.Fatalf("conflictResponse.Error = %q, want create prefix", conflictResponse.Error)
		}
		if !strings.Contains(conflictResponse.Error, "already exists") {
			t.Fatalf("conflictResponse.Error = %q, want conflict detail", conflictResponse.Error)
		}
	})
}

func TestGetRestoreStatus(t *testing.T) {
	t.Run("maps watched cluster state into ready progress", func(t *testing.T) {
		st := store.New()
		target := apiTestCluster("team-b", "alpha-restore")
		target.Status.Phase = "healthy"
		target.Status.PhaseReason = "ready"
		target.Status.ReadyInstances = target.Spec.Instances
		target.Status.CurrentPrimaryTimestamp = "2026-03-24T12:05:00Z"
		target.Status.Conditions = []metav1.Condition{{
			Type:               string(cnpgv1.ConditionClusterReady),
			Status:             metav1.ConditionTrue,
			Reason:             string(cnpgv1.ClusterReady),
			Message:            "cluster is ready",
			LastTransitionTime: metav1.NewTime(time.Date(2026, time.March, 24, 12, 10, 0, 0, time.UTC)),
		}}
		if err := st.UpsertCluster(target); err != nil {
			t.Fatalf("UpsertCluster(team-b/alpha-restore) error: %v", err)
		}

		router := NewRouter(ServerDeps{Store: st})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/clusters/team-b/alpha-restore/restore-status", nil)

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /api/clusters/team-b/alpha-restore/restore-status status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
		}

		var response RestoreStatusResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(restore status response): %v", err)
		}
		if got, want := response.Status.Phase, restorePhaseReady; got != want {
			t.Fatalf("response.Status.Phase = %q, want %q", got, want)
		}
		if response.Status.Timestamps.BootstrappingStartedAt == nil || response.Status.Timestamps.RecoveringStartedAt == nil || response.Status.Timestamps.ReadyAt == nil {
			t.Fatalf("response.Status.Timestamps = %#v, want bootstrapping/recovering/ready timestamps", response.Status.Timestamps)
		}
		if got, want := response.Status.PhaseReason, "ready"; got != want {
			t.Fatalf("response.Status.PhaseReason = %q, want %q", got, want)
		}
	})

	t.Run("surfaces failure diagnostics and redacts IP addresses", func(t *testing.T) {
		st := store.New()
		target := apiTestCluster("team-b", "alpha-restore")
		target.Status.Phase = "phase failed"
		target.Status.PhaseReason = "restore error"
		target.Status.CurrentPrimaryFailingSinceTimestamp = "2026-03-24T12:20:00Z"
		target.Status.Conditions = []metav1.Condition{{
			Type:               string(cnpgv1.ConditionClusterReady),
			Status:             metav1.ConditionFalse,
			Reason:             string(cnpgv1.ClusterIsNotReady),
			Message:            "restore job failed against 10.0.0.12:9187",
			LastTransitionTime: metav1.NewTime(time.Date(2026, time.March, 24, 12, 20, 0, 0, time.UTC)),
		}}
		if err := st.UpsertCluster(target); err != nil {
			t.Fatalf("UpsertCluster(team-b/alpha-restore) error: %v", err)
		}

		router := NewRouter(ServerDeps{Store: st})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/clusters/team-b/alpha-restore/restore-status", nil)

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /api/clusters/team-b/alpha-restore/restore-status status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
		}

		var response RestoreStatusResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(restore status response): %v", err)
		}
		if got, want := response.Status.Phase, restorePhaseFailed; got != want {
			t.Fatalf("response.Status.Phase = %q, want %q", got, want)
		}
		if response.Status.Timestamps.FailedAt == nil {
			t.Fatalf("response.Status.Timestamps = %#v, want failed timestamp", response.Status.Timestamps)
		}
		if strings.Contains(response.Status.Error, "10.0.0.12") {
			t.Fatalf("response.Status.Error leaked IP: %q", response.Status.Error)
		}
		if got, want := response.Status.Error, "restore job failed against <redacted>:9187"; got != want {
			t.Fatalf("response.Status.Error = %q, want %q", got, want)
		}
	})
}

type fakeRestoreCreator struct {
	calls             int
	lastSourceCluster *cnpgv1.Cluster
	lastBackup        *cnpgv1.Backup
	lastOptions       RestoreCreateOptions
	create            func(context.Context, *cnpgv1.Cluster, *cnpgv1.Backup, RestoreCreateOptions) (*cnpgv1.Cluster, error)
}

func (f *fakeRestoreCreator) CreateCluster(ctx context.Context, sourceCluster *cnpgv1.Cluster, backup *cnpgv1.Backup, options RestoreCreateOptions) (*cnpgv1.Cluster, error) {
	f.calls++
	if sourceCluster != nil {
		f.lastSourceCluster = sourceCluster.DeepCopy()
	}
	if backup != nil {
		f.lastBackup = backup.DeepCopy()
	}
	f.lastOptions = options
	if f.create != nil {
		return f.create(ctx, sourceCluster, backup, options)
	}
	return nil, nil
}

func apiRestoreTestBackup(namespace, name, clusterName string) *cnpgv1.Backup {
	backup := apiTestBackup(namespace, name, clusterName)
	backup.Spec.Method = cnpgv1.BackupMethodBarmanObjectStore
	backup.Status.Method = cnpgv1.BackupMethodBarmanObjectStore
	backup.Status.Phase = cnpgv1.BackupPhaseCompleted
	backup.Status.BackupID = "20260324T110000"
	backup.Status.DestinationPath = "s3://deckhand/backups"
	backup.Status.ServerName = clusterName
	return backup
}

var _ RestoreCreator = (*fakeRestoreCreator)(nil)
