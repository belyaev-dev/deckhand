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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/deckhand-for-cnpg/deckhand/internal/store"
)

func TestListBackups(t *testing.T) {
	st := store.New()
	if err := st.UpsertCluster(apiTestClusterWithBackupConfig("team-a", "alpha")); err != nil {
		t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
	}
	if err := st.UpsertCluster(apiTestClusterWithBackupConfig("team-b", "bravo")); err != nil {
		t.Fatalf("UpsertCluster(team-b/bravo) error: %v", err)
	}
	if err := st.UpsertBackup(apiTestBackup("team-a", "alpha-backup", "alpha")); err != nil {
		t.Fatalf("UpsertBackup(team-a/alpha-backup) error: %v", err)
	}
	if err := st.UpsertBackup(apiTestBackup("team-a", "other-backup", "other")); err != nil {
		t.Fatalf("UpsertBackup(team-a/other-backup) error: %v", err)
	}
	if err := st.UpsertBackup(apiTestBackup("team-b", "bravo-backup", "bravo")); err != nil {
		t.Fatalf("UpsertBackup(team-b/bravo-backup) error: %v", err)
	}
	if err := st.UpsertScheduledBackup(apiTestScheduledBackup("team-a", "alpha-nightly", "alpha")); err != nil {
		t.Fatalf("UpsertScheduledBackup(team-a/alpha-nightly) error: %v", err)
	}
	if err := st.UpsertScheduledBackup(apiTestScheduledBackup("team-a", "other-nightly", "other")); err != nil {
		t.Fatalf("UpsertScheduledBackup(team-a/other-nightly) error: %v", err)
	}

	router := NewRouter(ServerDeps{Store: st})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/clusters/team-a/alpha/backups", nil)

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/clusters/team-a/alpha/backups status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}

	var response ClusterBackupsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(backups response): %v", err)
	}
	if response.Cluster.Namespace != "team-a" || response.Cluster.Name != "alpha" {
		t.Fatalf("response.Cluster = %#v, want namespace=%q name=%q", response.Cluster, "team-a", "alpha")
	}
	if got := len(response.Backups); got != 1 {
		t.Fatalf("len(response.Backups) = %d, want 1", got)
	}
	if got := len(response.ScheduledBackups); got != 1 {
		t.Fatalf("len(response.ScheduledBackups) = %d, want 1", got)
	}
	if got, want := response.Backups[0].ClusterName, "alpha"; got != want {
		t.Fatalf("response.Backups[0].ClusterName = %q, want %q", got, want)
	}
	if got, want := response.ScheduledBackups[0].ClusterName, "alpha"; got != want {
		t.Fatalf("response.ScheduledBackups[0].ClusterName = %q, want %q", got, want)
	}
	for _, fragment := range []string{"backup-creds", "secretAccessKey", "superuserSecret", "endpointCA"} {
		if bytes.Contains(recorder.Body.Bytes(), []byte(fragment)) {
			t.Fatalf("response leaked sensitive fragment %q: %s", fragment, recorder.Body.String())
		}
	}
}

func TestCreateBackup(t *testing.T) {
	t.Run("defaults method and target and returns the created backup summary", func(t *testing.T) {
		st := store.New()
		cluster := apiTestClusterWithBackupConfig("team-a", "alpha")
		cluster.Spec.Backup.Target = cnpgv1.BackupTargetPrimary
		if err := st.UpsertCluster(cluster); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}

		creator := &fakeBackupCreator{
			create: func(_ context.Context, cluster *cnpgv1.Cluster, options BackupCreateOptions) (*cnpgv1.Backup, error) {
				createdAt := metav1.NewTime(time.Date(2026, time.March, 24, 12, 30, 0, 0, time.UTC))
				startedAt := metav1.NewTime(time.Date(2026, time.March, 24, 12, 31, 0, 0, time.UTC))
				return &cnpgv1.Backup{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:         cluster.Namespace,
						Name:              "alpha-backup-manual-001",
						CreationTimestamp: createdAt,
					},
					Spec: cnpgv1.BackupSpec{
						Cluster: cnpgv1.LocalObjectReference{Name: cluster.Name},
						Method:  options.Method,
						Target:  options.Target,
					},
					Status: cnpgv1.BackupStatus{
						Phase:     cnpgv1.BackupPhaseRunning,
						Method:    options.Method,
						StartedAt: &startedAt,
						EndpointCA: &cnpgv1.SecretKeySelector{
							LocalObjectReference: cnpgv1.LocalObjectReference{Name: "backup-ca"},
							Key:                  "ca.crt",
						},
					},
				}, nil
			},
		}

		router := NewRouter(ServerDeps{Store: st, BackupCreator: creator})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/clusters/team-a/alpha/backups", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusCreated {
			t.Fatalf("POST /api/clusters/team-a/alpha/backups status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
		}
		if creator.calls != 1 {
			t.Fatalf("creator.calls = %d, want 1", creator.calls)
		}
		if got, want := creator.lastCluster.Namespace, "team-a"; got != want {
			t.Fatalf("creator.lastCluster.Namespace = %q, want %q", got, want)
		}
		if got, want := creator.lastCluster.Name, "alpha"; got != want {
			t.Fatalf("creator.lastCluster.Name = %q, want %q", got, want)
		}
		if got, want := creator.lastOptions.Method, cnpgv1.BackupMethodBarmanObjectStore; got != want {
			t.Fatalf("creator.lastOptions.Method = %q, want %q", got, want)
		}
		if got, want := creator.lastOptions.Target, cnpgv1.BackupTargetPrimary; got != want {
			t.Fatalf("creator.lastOptions.Target = %q, want %q", got, want)
		}

		var response CreateBackupResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(create response): %v", err)
		}
		if got, want := response.Backup.Name, "alpha-backup-manual-001"; got != want {
			t.Fatalf("response.Backup.Name = %q, want %q", got, want)
		}
		if got, want := response.Backup.Method, string(cnpgv1.BackupMethodBarmanObjectStore); got != want {
			t.Fatalf("response.Backup.Method = %q, want %q", got, want)
		}
		if got, want := response.Backup.Target, string(cnpgv1.BackupTargetPrimary); got != want {
			t.Fatalf("response.Backup.Target = %q, want %q", got, want)
		}
		if got, want := response.Backup.Phase, string(cnpgv1.BackupPhaseRunning); got != want {
			t.Fatalf("response.Backup.Phase = %q, want %q", got, want)
		}
		if bytes.Contains(recorder.Body.Bytes(), []byte("backup-ca")) {
			t.Fatalf("response leaked backup secret reference: %s", recorder.Body.String())
		}
	})

	t.Run("returns 400 for invalid requests", func(t *testing.T) {
		st := store.New()
		if err := st.UpsertCluster(apiTestClusterWithBackupConfig("team-a", "alpha")); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}

		router := NewRouter(ServerDeps{Store: st, BackupCreator: &fakeBackupCreator{}})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/clusters/team-a/alpha/backups", strings.NewReader(`{"target":"replica"}`))
		request.Header.Set("Content-Type", "application/json")

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("POST invalid target status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
		var response ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(error response): %v", err)
		}
		if got, want := response.Error, `backup target "replica" is not supported`; got != want {
			t.Fatalf("response.Error = %q, want %q", got, want)
		}
	})

	t.Run("returns 404 when the cluster is missing", func(t *testing.T) {
		creator := &fakeBackupCreator{}
		router := NewRouter(ServerDeps{Store: store.New(), BackupCreator: creator})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/clusters/team-a/missing/backups", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusNotFound {
			t.Fatalf("POST missing cluster status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
		}
		if creator.calls != 0 {
			t.Fatalf("creator.calls = %d, want 0", creator.calls)
		}
	})

	t.Run("returns 409 when the cluster is not eligible for the requested backup method", func(t *testing.T) {
		st := store.New()
		cluster := apiTestCluster("team-a", "alpha")
		cluster.Spec.Backup = &cnpgv1.BackupConfiguration{}
		if err := st.UpsertCluster(cluster); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}

		router := NewRouter(ServerDeps{Store: st, BackupCreator: &fakeBackupCreator{}})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/clusters/team-a/alpha/backups", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusConflict {
			t.Fatalf("POST ineligible cluster status = %d, want %d; body=%s", recorder.Code, http.StatusConflict, recorder.Body.String())
		}
		var response ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(error response): %v", err)
		}
		if got, want := response.Error, `cluster "alpha" in namespace "team-a" is not configured for backups`; got != want {
			t.Fatalf("response.Error = %q, want %q", got, want)
		}
	})

	t.Run("returns 409 when the creator reports a conflict", func(t *testing.T) {
		st := store.New()
		if err := st.UpsertCluster(apiTestClusterWithBackupConfig("team-a", "alpha")); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}

		creator := &fakeBackupCreator{
			create: func(_ context.Context, _ *cnpgv1.Cluster, _ BackupCreateOptions) (*cnpgv1.Backup, error) {
				return nil, apierrors.NewConflict(schema.GroupResource{Group: cnpgv1.SchemeGroupVersion.Group, Resource: "backups"}, "alpha-backup", errors.New("backup already exists"))
			},
		}
		router := NewRouter(ServerDeps{Store: st, BackupCreator: creator})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/clusters/team-a/alpha/backups", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusConflict {
			t.Fatalf("POST creator conflict status = %d, want %d; body=%s", recorder.Code, http.StatusConflict, recorder.Body.String())
		}
		var response ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(error response): %v", err)
		}
		if !strings.Contains(response.Error, `create backup for cluster "alpha" in namespace "team-a":`) {
			t.Fatalf("response.Error = %q, want create prefix", response.Error)
		}
		if !strings.Contains(response.Error, "already exists") {
			t.Fatalf("response.Error = %q, want conflict detail", response.Error)
		}
	})
}

type fakeBackupCreator struct {
	calls       int
	lastCluster *cnpgv1.Cluster
	lastOptions BackupCreateOptions
	create      func(context.Context, *cnpgv1.Cluster, BackupCreateOptions) (*cnpgv1.Backup, error)
}

func (f *fakeBackupCreator) CreateBackup(ctx context.Context, cluster *cnpgv1.Cluster, options BackupCreateOptions) (*cnpgv1.Backup, error) {
	f.calls++
	if cluster != nil {
		f.lastCluster = cluster.DeepCopy()
	}
	f.lastOptions = options
	if f.create != nil {
		return f.create(ctx, cluster, options)
	}
	return nil, nil
}

func apiTestClusterWithBackupConfig(namespace, name string) *cnpgv1.Cluster {
	cluster := apiTestCluster(namespace, name)
	cluster.Spec.Backup = &cnpgv1.BackupConfiguration{
		Target: cnpgv1.BackupTargetStandby,
		BarmanObjectStore: &cnpgv1.BarmanObjectStoreConfiguration{
			DestinationPath: "s3://deckhand-test/backups",
			BarmanCredentials: cnpgv1.BarmanCredentials{
				AWS: &cnpgv1.S3Credentials{InheritFromIAMRole: true},
			},
		},
	}
	return cluster
}
