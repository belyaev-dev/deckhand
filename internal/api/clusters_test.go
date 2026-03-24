package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	barmanapi "github.com/cloudnative-pg/barman-cloud/pkg/api"
	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	machineryapi "github.com/cloudnative-pg/machinery/pkg/api"
	"github.com/deckhand-for-cnpg/deckhand/internal/metrics"
	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestListClustersIncludesOverviewHealth(t *testing.T) {
	st := store.New()
	if err := st.UpsertCluster(apiTestCluster("team-b", "bravo")); err != nil {
		t.Fatalf("UpsertCluster(team-b/bravo) error: %v", err)
	}
	if err := st.UpsertCluster(apiTestCluster("team-a", "alpha")); err != nil {
		t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
	}

	scrapedAt := time.Date(2026, time.March, 24, 13, 0, 0, 0, time.UTC)
	reader := overviewFakeMetricsReader{clusters: map[string]metrics.ClusterMetrics{
		clusterMetricsKey("team-a", "alpha"): {
			Namespace:     "team-a",
			ClusterName:   "alpha",
			OverallHealth: metrics.Warning,
			ScrapedAt:     scrapedAt,
			ScrapeError:   "alpha scrape http://10.0.0.12:9187/metrics degraded",
		},
	}}

	router := NewRouter(ServerDeps{Store: st, MetricsReader: reader})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/clusters", nil)

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/clusters status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}

	var response ClusterListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(list response): %v", err)
	}
	if got := len(response.Items); got != 2 {
		t.Fatalf("len(response.Items) = %d, want 2", got)
	}

	alpha := response.Items[0]
	if alpha.Namespace != "team-a" || alpha.Name != "alpha" {
		t.Fatalf("first item = %#v, want namespace=%q name=%q", alpha, "team-a", "alpha")
	}
	if got, want := alpha.OverallHealth, "warning"; got != want {
		t.Fatalf("alpha.OverallHealth = %q, want %q", got, want)
	}
	if alpha.MetricsScrapedAt == nil || !alpha.MetricsScrapedAt.Equal(scrapedAt) {
		t.Fatalf("alpha.MetricsScrapedAt = %#v, want %s", alpha.MetricsScrapedAt, scrapedAt)
	}
	if got, want := alpha.MetricsScrapeError, "alpha scrape http://<redacted>:9187/metrics degraded"; got != want {
		t.Fatalf("alpha.MetricsScrapeError = %q, want %q", got, want)
	}
	if alpha.FirstRecoverabilityPoint == nil || alpha.LastSuccessfulBackup == nil {
		t.Fatalf("alpha backup timestamps = %#v, want non-nil pointers", alpha)
	}

	bravo := response.Items[1]
	if bravo.Namespace != "team-b" || bravo.Name != "bravo" {
		t.Fatalf("second item = %#v, want namespace=%q name=%q", bravo, "team-b", "bravo")
	}
	if got, want := bravo.OverallHealth, "unknown"; got != want {
		t.Fatalf("bravo.OverallHealth = %q, want %q", got, want)
	}
	if bravo.MetricsScrapedAt != nil {
		t.Fatalf("bravo.MetricsScrapedAt = %#v, want nil", bravo.MetricsScrapedAt)
	}
	if got, want := bravo.MetricsScrapeError, "metrics not available yet"; got != want {
		t.Fatalf("bravo.MetricsScrapeError = %q, want %q", got, want)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("postgres-superuser")) {
		t.Fatalf("response leaked secret reference: %s", recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("10.0.0.12")) {
		t.Fatalf("response leaked pod IP: %s", recorder.Body.String())
	}
}

func TestListClustersExposesNamespaces(t *testing.T) {
	t.Run("returns sorted namespace metadata with cluster counts", func(t *testing.T) {
		st := store.New()
		for _, cluster := range []*cnpgv1.Cluster{
			apiTestCluster("team-b", "bravo"),
			apiTestCluster("team-a", "alpha"),
			apiTestCluster("team-a", "charlie"),
		} {
			if err := st.UpsertCluster(cluster); err != nil {
				t.Fatalf("UpsertCluster(%s/%s) error: %v", cluster.Namespace, cluster.Name, err)
			}
		}

		router := NewRouter(ServerDeps{Store: st})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/clusters", nil)

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /api/clusters status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
		}

		var response ClusterListResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(list response): %v", err)
		}

		wantNamespaces := []ClusterNamespaceSummary{
			{Name: "team-a", ClusterCount: 2},
			{Name: "team-b", ClusterCount: 1},
		}
		if len(response.Namespaces) != len(wantNamespaces) {
			t.Fatalf("len(response.Namespaces) = %d, want %d", len(response.Namespaces), len(wantNamespaces))
		}
		for i, want := range wantNamespaces {
			if got := response.Namespaces[i]; got != want {
				t.Fatalf("response.Namespaces[%d] = %#v, want %#v", i, got, want)
			}
		}

		if got := len(response.Items); got != 3 {
			t.Fatalf("len(response.Items) = %d, want 3", got)
		}
		if got, want := response.Items[0].Name, "alpha"; got != want {
			t.Fatalf("response.Items[0].Name = %q, want %q", got, want)
		}
		if got, want := response.Items[1].Name, "charlie"; got != want {
			t.Fatalf("response.Items[1].Name = %q, want %q", got, want)
		}
		if got, want := response.Items[2].Name, "bravo"; got != want {
			t.Fatalf("response.Items[2].Name = %q, want %q", got, want)
		}
	})

	t.Run("returns filtered namespace metadata and explicit empty arrays", func(t *testing.T) {
		st := store.New()
		if err := st.UpsertCluster(apiTestCluster("team-a", "alpha")); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}
		if err := st.UpsertCluster(apiTestCluster("team-b", "bravo")); err != nil {
			t.Fatalf("UpsertCluster(team-b/bravo) error: %v", err)
		}

		router := NewRouter(ServerDeps{Store: st})

		filteredRecorder := httptest.NewRecorder()
		filteredRequest := httptest.NewRequest(http.MethodGet, "/api/clusters?namespace=team-a", nil)
		router.ServeHTTP(filteredRecorder, filteredRequest)

		if filteredRecorder.Code != http.StatusOK {
			t.Fatalf("GET /api/clusters?namespace=team-a status = %d, want %d; body=%s", filteredRecorder.Code, http.StatusOK, filteredRecorder.Body.String())
		}

		var filteredResponse ClusterListResponse
		if err := json.Unmarshal(filteredRecorder.Body.Bytes(), &filteredResponse); err != nil {
			t.Fatalf("json.Unmarshal(filtered response): %v", err)
		}
		if got := len(filteredResponse.Items); got != 1 {
			t.Fatalf("len(filteredResponse.Items) = %d, want 1", got)
		}
		if got := len(filteredResponse.Namespaces); got != 1 {
			t.Fatalf("len(filteredResponse.Namespaces) = %d, want 1", got)
		}
		if got, want := filteredResponse.Namespaces[0].Name, "team-a"; got != want {
			t.Fatalf("filteredResponse.Namespaces[0].Name = %q, want %q", got, want)
		}

		emptyRecorder := httptest.NewRecorder()
		emptyRequest := httptest.NewRequest(http.MethodGet, "/api/clusters?namespace=missing", nil)
		router.ServeHTTP(emptyRecorder, emptyRequest)

		if emptyRecorder.Code != http.StatusOK {
			t.Fatalf("GET /api/clusters?namespace=missing status = %d, want %d; body=%s", emptyRecorder.Code, http.StatusOK, emptyRecorder.Body.String())
		}
		if !bytes.Contains(emptyRecorder.Body.Bytes(), []byte(`"items":[]`)) {
			t.Fatalf("expected explicit empty items array, got %s", emptyRecorder.Body.String())
		}
		if !bytes.Contains(emptyRecorder.Body.Bytes(), []byte(`"namespaces":[]`)) {
			t.Fatalf("expected explicit empty namespaces array, got %s", emptyRecorder.Body.String())
		}
	})
}

func TestGetCluster(t *testing.T) {
	t.Run("returns cluster detail with redacted backup fields", func(t *testing.T) {
		st := store.New()
		if err := st.UpsertCluster(apiTestCluster("team-a", "alpha")); err != nil {
			t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
		}
		if err := st.UpsertBackup(apiTestBackup("team-a", "alpha-backup", "alpha")); err != nil {
			t.Fatalf("UpsertBackup(team-a/alpha-backup) error: %v", err)
		}
		if err := st.UpsertScheduledBackup(apiTestScheduledBackup("team-a", "alpha-nightly", "alpha")); err != nil {
			t.Fatalf("UpsertScheduledBackup(team-a/alpha-nightly) error: %v", err)
		}
		if err := st.UpsertBackup(apiTestBackup("team-a", "other-backup", "other")); err != nil {
			t.Fatalf("UpsertBackup(team-a/other-backup) error: %v", err)
		}

		router := NewRouter(ServerDeps{Store: st})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/clusters/team-a/alpha", nil)

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /api/clusters/team-a/alpha status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
		}

		var response ClusterDetailResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(detail response): %v", err)
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
		if response.Backups[0].ClusterName != "alpha" {
			t.Fatalf("response.Backups[0].ClusterName = %q, want %q", response.Backups[0].ClusterName, "alpha")
		}
		for _, fragment := range []string{"backup-creds", "secretAccessKey", "superuserSecret", "endpointCA"} {
			if bytes.Contains(recorder.Body.Bytes(), []byte(fragment)) {
				t.Fatalf("response leaked sensitive fragment %q: %s", fragment, recorder.Body.String())
			}
		}
	})

	t.Run("returns explicit 404 when cluster is missing", func(t *testing.T) {
		router := NewRouter(ServerDeps{Store: store.New()})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/clusters/team-a/missing", nil)

		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET /api/clusters/team-a/missing status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
		}

		var response ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("json.Unmarshal(error response): %v", err)
		}
		wantError := `cluster "missing" in namespace "team-a" not found`
		if response.Error != wantError {
			t.Fatalf("response.Error = %q, want %q", response.Error, wantError)
		}
	})
}

type overviewFakeMetricsReader struct {
	clusters map[string]metrics.ClusterMetrics
}

func (f overviewFakeMetricsReader) GetClusterMetrics(namespace, name string) (*metrics.ClusterMetrics, bool) {
	cached, ok := f.clusters[clusterMetricsKey(namespace, name)]
	if !ok {
		return nil, false
	}
	copy := cached
	copy.Instances = append([]metrics.InstanceMetrics(nil), cached.Instances...)
	return &copy, true
}

func clusterMetricsKey(namespace, name string) string {
	return namespace + "/" + name
}

func apiTestCluster(namespace, name string) *cnpgv1.Cluster {
	createdAt := metav1.NewTime(time.Date(2026, time.March, 24, 12, 0, 0, 0, time.UTC))
	firstRecovery := metav1.NewTime(time.Date(2026, time.March, 24, 10, 0, 0, 0, time.UTC))
	lastBackup := metav1.NewTime(time.Date(2026, time.March, 24, 11, 30, 0, 0, time.UTC))
	return &cnpgv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: createdAt,
		},
		Spec: cnpgv1.ClusterSpec{
			Instances:       3,
			ImageName:       "ghcr.io/cloudnative-pg/postgresql:16.3",
			SuperuserSecret: &cnpgv1.LocalObjectReference{Name: "postgres-superuser"},
		},
		Status: cnpgv1.ClusterStatus{
			Phase:          "setting up primary",
			PhaseReason:    "bootstrapping",
			ReadyInstances: 2,
			CurrentPrimary: name + "-1",
			Image:          "ghcr.io/cloudnative-pg/postgresql:16.3",
			FirstRecoverabilityPointByMethod: map[cnpgv1.BackupMethod]metav1.Time{
				cnpgv1.BackupMethod("barmanObjectStore"): firstRecovery,
			},
			LastSuccessfulBackupByMethod: map[cnpgv1.BackupMethod]metav1.Time{
				cnpgv1.BackupMethod("barmanObjectStore"): lastBackup,
			},
		},
	}
}

func apiTestBackup(namespace, name, clusterName string) *cnpgv1.Backup {
	createdAt := metav1.NewTime(time.Date(2026, time.March, 24, 11, 0, 0, 0, time.UTC))
	stoppedAt := metav1.NewTime(time.Date(2026, time.March, 24, 11, 5, 0, 0, time.UTC))
	return &cnpgv1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: createdAt,
		},
		Spec: cnpgv1.BackupSpec{
			Cluster: cnpgv1.LocalObjectReference{Name: clusterName},
			Target:  cnpgv1.BackupTarget("primary"),
			Method:  cnpgv1.BackupMethod("barmanObjectStore"),
		},
		Status: cnpgv1.BackupStatus{
			BarmanCredentials: cnpgv1.BarmanCredentials{
				AWS: &barmanapi.S3Credentials{
					AccessKeyIDReference:     &machineryapi.SecretKeySelector{LocalObjectReference: machineryapi.LocalObjectReference{Name: "backup-creds"}, Key: "accessKeyID"},
					SecretAccessKeyReference: &machineryapi.SecretKeySelector{LocalObjectReference: machineryapi.LocalObjectReference{Name: "backup-creds"}, Key: "secretAccessKey"},
				},
			},
			EndpointCA: &cnpgv1.SecretKeySelector{LocalObjectReference: cnpgv1.LocalObjectReference{Name: "backup-ca"}, Key: "ca.crt"},
			Phase:      cnpgv1.BackupPhase("completed"),
			Method:     cnpgv1.BackupMethod("barmanObjectStore"),
			StartedAt:  &createdAt,
			StoppedAt:  &stoppedAt,
			Error:      "",
		},
	}
}

func apiTestScheduledBackup(namespace, name, clusterName string) *cnpgv1.ScheduledBackup {
	createdAt := metav1.NewTime(time.Date(2026, time.March, 24, 8, 0, 0, 0, time.UTC))
	lastSchedule := metav1.NewTime(time.Date(2026, time.March, 24, 9, 0, 0, 0, time.UTC))
	nextSchedule := metav1.NewTime(time.Date(2026, time.March, 25, 9, 0, 0, 0, time.UTC))
	immediate := true
	suspend := false
	return &cnpgv1.ScheduledBackup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: createdAt,
		},
		Spec: cnpgv1.ScheduledBackupSpec{
			Cluster:   cnpgv1.LocalObjectReference{Name: clusterName},
			Schedule:  "0 0 */6 * * *",
			Method:    cnpgv1.BackupMethod("barmanObjectStore"),
			Immediate: &immediate,
			Suspend:   &suspend,
		},
		Status: cnpgv1.ScheduledBackupStatus{
			LastScheduleTime: &lastSchedule,
			NextScheduleTime: &nextSchedule,
		},
	}
}
