# Deckhand API reference

Deckhand serves a JSON REST API plus a WebSocket invalidation stream from the same process that hosts the embedded React UI. The authoritative route registration lives in `internal/api/server.go`, and the stable DTOs live in `internal/api/types.go` plus `web/src/types/api.ts`.

## Base behavior

- REST responses use `application/json`.
- API errors use the stable envelope `{"error":"..."}`.
- Unknown API routes return JSON `404` responses with `{"error":"route not found"}` instead of falling through to the SPA.
- Browser-facing payloads are redacted: raw CRDs, pod IPs, secret references, and backup credentials are intentionally omitted.

## Surface summary

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | Liveness/readiness probe used by the Deployment probes. |
| `GET` | `/api` | Version document for the Deckhand API surface. |
| `GET` | `/api/clusters` | Cluster overview list, optionally filtered by namespace. |
| `GET` | `/api/clusters/{namespace}/{name}` | Cluster detail summary plus backup and scheduled-backup history. |
| `GET` | `/api/clusters/{namespace}/{name}/metrics` | Per-instance metrics snapshot and scrape health. |
| `GET` | `/api/clusters/{namespace}/{name}/backups` | Backup history plus scheduled backup policies for one cluster. |
| `POST` | `/api/clusters/{namespace}/{name}/backups` | Create an on-demand CNPG `Backup`. |
| `GET` | `/api/clusters/{namespace}/{name}/restore` | Restore candidates, recoverability window, and supported restore phases. |
| `POST` | `/api/clusters/{namespace}/{name}/restore` | Create a restore target `Cluster` from a selected backup. |
| `GET` | `/api/clusters/{namespace}/{name}/restore-status` | Guided restore progress for a target cluster. |
| `GET` | `/ws` | WebSocket invalidation stream for live refetches. |

## `GET /healthz`

Health probe used by the Helm chart's liveness/readiness checks.

### Example response

```json
{"status":"ok"}
```

## `GET /api`

Returns the current API version document. Internally this is mounted as `r.Get("/")` under the `/api` router.

### Example response

```json
{"version":"0.1.0"}
```

## `GET /api/clusters`

Returns the overview page data. Use the optional `namespace` query parameter to filter the list without changing the response shape.

### Query parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| `namespace` | string | No | Restrict the list to one namespace. |

### Example request

```text
GET /api/clusters?namespace=team-a
```

### Example response

```json
{
  "namespaces": [
    { "name": "team-a", "clusterCount": 1 }
  ],
  "items": [
    {
      "namespace": "team-a",
      "name": "alpha",
      "createdAt": "2026-03-24T12:00:00Z",
      "phase": "setting up primary",
      "phaseReason": "bootstrapping",
      "desiredInstances": 3,
      "readyInstances": 2,
      "currentPrimary": "alpha-1",
      "image": "ghcr.io/cloudnative-pg/postgresql:16.3",
      "firstRecoverabilityPoint": "2026-03-24T10:00:00Z",
      "lastSuccessfulBackup": "2026-03-24T11:30:00Z",
      "overallHealth": "warning",
      "metricsScrapedAt": "2026-03-24T13:00:00Z",
      "metricsScrapeError": "alpha scrape http://<redacted>:9187/metrics degraded"
    }
  ]
}
```

### Notes

- Empty results still return explicit arrays: `{"namespaces":[],"items":[]}`.
- `metricsScrapeError` is populated even when metrics are unavailable; the fallback value is `metrics not available yet`.

## `GET /api/clusters/{namespace}/{name}`

Returns the detail page payload for one watched CNPG cluster.

### Example response

```json
{
  "cluster": {
    "namespace": "team-a",
    "name": "alpha",
    "createdAt": "2026-03-24T12:00:00Z",
    "phase": "setting up primary",
    "phaseReason": "bootstrapping",
    "desiredInstances": 3,
    "readyInstances": 2,
    "currentPrimary": "alpha-1",
    "image": "ghcr.io/cloudnative-pg/postgresql:16.3",
    "firstRecoverabilityPoint": "2026-03-24T10:00:00Z",
    "lastSuccessfulBackup": "2026-03-24T11:30:00Z"
  },
  "backups": [
    {
      "namespace": "team-a",
      "name": "alpha-backup",
      "clusterName": "alpha",
      "createdAt": "2026-03-24T11:00:00Z",
      "phase": "completed",
      "method": "barmanObjectStore",
      "target": "primary",
      "startedAt": "2026-03-24T11:00:00Z",
      "stoppedAt": "2026-03-24T11:05:00Z"
    }
  ],
  "scheduledBackups": [
    {
      "namespace": "team-a",
      "name": "alpha-nightly",
      "clusterName": "alpha",
      "createdAt": "2026-03-24T08:00:00Z",
      "schedule": "0 0 */6 * * *",
      "method": "barmanObjectStore",
      "immediate": true,
      "suspended": false,
      "lastScheduleTime": "2026-03-24T09:00:00Z",
      "nextScheduleTime": "2026-03-25T09:00:00Z"
    }
  ]
}
```

### Error behavior

If the cluster is not present in the in-memory store, Deckhand returns `404`:

```json
{
  "error": "cluster \"missing\" in namespace \"team-a\" not found"
}
```

## `GET /api/clusters/{namespace}/{name}/metrics`

Returns the latest cached per-instance metrics snapshot for one cluster.

### Example response

```json
{
  "cluster": {
    "namespace": "team-a",
    "name": "alpha",
    "createdAt": "2026-03-24T12:00:00Z",
    "phase": "setting up primary",
    "phaseReason": "bootstrapping",
    "desiredInstances": 3,
    "readyInstances": 2,
    "currentPrimary": "alpha-1",
    "image": "ghcr.io/cloudnative-pg/postgresql:16.3",
    "firstRecoverabilityPoint": "2026-03-24T10:00:00Z",
    "lastSuccessfulBackup": "2026-03-24T11:30:00Z"
  },
  "overallHealth": "warning",
  "scrapedAt": "2026-03-24T13:00:00Z",
  "scrapeError": "alpha-2 scrape http://<redacted>:9187/metrics degraded",
  "instances": [
    {
      "podName": "alpha-1",
      "podStatus": "healthy",
      "health": "healthy",
      "connections": {
        "active": 4,
        "idle": 6,
        "idleInTransaction": 0,
        "total": 10,
        "maxConnections": 100
      },
      "replication": {
        "replicationLagSeconds": 2,
        "isReplica": false,
        "isWalReceiverUp": true,
        "streamingReplicas": 1,
        "replayLagBytes": 1024
      },
      "disk": {
        "pvcCapacityBytes": 21474836480,
        "databaseSizeBytes": 8589934592
      },
      "scrapedAt": "2026-03-24T13:00:00Z"
    },
    {
      "podName": "alpha-2",
      "podStatus": "failed",
      "health": "unknown",
      "connections": {
        "active": 0,
        "idle": 0,
        "idleInTransaction": 0,
        "total": 0,
        "maxConnections": 0
      },
      "replication": {
        "replicationLagSeconds": 0,
        "isReplica": false,
        "isWalReceiverUp": false,
        "streamingReplicas": 0,
        "replayLagBytes": 0
      },
      "disk": {
        "pvcCapacityBytes": 10737418240,
        "databaseSizeBytes": 0
      },
      "scrapedAt": "2026-03-24T13:00:00Z",
      "scrapeError": "scrape http://<redacted>:9187/metrics: connection refused"
    }
  ]
}
```

### Notes

- When a cluster exists but the metrics cache has no entry yet, Deckhand still returns `200` with `overallHealth: "unknown"`, `scrapeError: "metrics not available yet"`, and `instances: []`.
- Pod IPs and raw exporter output are redacted from the browser contract.

## `GET /api/clusters/{namespace}/{name}/backups`

Returns the backup-management page payload for one cluster.

### Example response

The response shape matches `GET /api/clusters/{namespace}/{name}`:

- `cluster`: cluster summary
- `backups`: backup history for the selected cluster only
- `scheduledBackups`: scheduled backup policies for the selected cluster only

### Notes

- Backups from other clusters in the same namespace are filtered out.
- Secret references such as `backup-creds`, `secretAccessKey`, `superuserSecret`, and `endpointCA` are intentionally excluded.

## `POST /api/clusters/{namespace}/{name}/backups`

Creates an on-demand CNPG `Backup` for the selected cluster.

### Request body

```json
{
  "method": "barmanObjectStore",
  "target": "primary"
}
```

Both fields are optional:

- `method` defaults from the CNPG cluster backup configuration.
- `target` defaults to the cluster backup target or CNPG's default target.

Supported values:

- `method`: `barmanObjectStore`, `volumeSnapshot`
- `target`: `primary`, `prefer-standby`

### Example success response (`201 Created`)

```json
{
  "backup": {
    "namespace": "team-a",
    "name": "alpha-backup-manual-001",
    "clusterName": "alpha",
    "createdAt": "2026-03-24T12:30:00Z",
    "phase": "running",
    "method": "barmanObjectStore",
    "target": "primary",
    "startedAt": "2026-03-24T12:31:00Z"
  }
}
```

### Common error responses

| Status | When it happens | Example |
| --- | --- | --- |
| `400` | Invalid JSON, unsupported method, or unsupported target. | `{"error":"backup target \"replica\" is not supported"}` |
| `404` | Source cluster is not in the store. | `{"error":"cluster \"missing\" in namespace \"team-a\" not found"}` |
| `409` | The cluster is not configured for the requested backup method, or the creator reports a conflict. | `{"error":"cluster \"alpha\" in namespace \"team-a\" is not configured for backups"}` |
| `503` | No backup creator was wired into the API server. | `{"error":"backup creation is not configured"}` |

## `GET /api/clusters/{namespace}/{name}/restore`

Returns the inputs needed to render the guided restore flow.

### Example response

```json
{
  "cluster": {
    "namespace": "team-a",
    "name": "alpha",
    "createdAt": "2026-03-24T12:00:00Z",
    "phase": "setting up primary",
    "phaseReason": "bootstrapping",
    "desiredInstances": 3,
    "readyInstances": 2,
    "currentPrimary": "alpha-1",
    "image": "ghcr.io/cloudnative-pg/postgresql:16.3",
    "firstRecoverabilityPoint": "2026-03-24T10:00:00Z",
    "lastSuccessfulBackup": "2026-03-24T11:30:00Z"
  },
  "backups": [
    {
      "namespace": "team-a",
      "name": "alpha-backup-20260324",
      "clusterName": "alpha",
      "createdAt": "2026-03-24T11:00:00Z",
      "phase": "completed",
      "method": "barmanObjectStore",
      "target": "primary",
      "startedAt": "2026-03-24T11:00:00Z",
      "stoppedAt": "2026-03-24T11:05:00Z"
    }
  ],
  "recoverability": {
    "start": "2026-03-24T10:00:00Z",
    "end": "2026-03-24T11:30:00Z"
  },
  "supportedPhases": ["bootstrapping", "recovering", "ready", "failed"]
}
```

### Notes

- Only backups for the selected source cluster are returned.
- `recoverability.start` and `recoverability.end` come from the source cluster's advertised first recoverability point and last successful backup.
- The `supportedPhases` list is the same phase model used by `GET /api/clusters/{namespace}/{name}/restore-status`.

## `POST /api/clusters/{namespace}/{name}/restore`

Creates a new restore target `Cluster` from a completed backup.

### Request body

```json
{
  "backupName": "alpha-backup-20260324",
  "targetNamespace": "team-b",
  "targetName": "alpha-restore",
  "pitrTargetTime": "2026-03-24T11:15:00Z"
}
```

### Field rules

- `backupName`, `targetNamespace`, and `targetName` are required.
- `targetNamespace` and `targetName` must be lowercase RFC 1123 DNS labels.
- The restore target must not overwrite the source cluster.
- `pitrTargetTime` is optional, but when provided it must:
  - be RFC 3339
  - use an object-store backup (`barmanObjectStore`)
  - fall within the advertised recoverability window

### Example success response (`201 Created`)

```json
{
  "sourceCluster": {
    "namespace": "team-a",
    "name": "alpha",
    "desiredInstances": 3,
    "readyInstances": 2
  },
  "targetCluster": {
    "namespace": "team-b",
    "name": "alpha-restore",
    "desiredInstances": 3,
    "readyInstances": 0
  },
  "backup": {
    "namespace": "team-a",
    "name": "alpha-backup-20260324",
    "clusterName": "alpha",
    "phase": "completed",
    "method": "barmanObjectStore",
    "target": "primary"
  },
  "yamlPreview": "apiVersion: postgresql.cnpg.io/v1\nkind: Cluster\nmetadata:\n  name: alpha-restore\n  namespace: team-b\nspec:\n  bootstrap:\n    recovery:\n      source: alpha\n      recoveryTarget:\n        backupID: \"20260324T110000\"\n        targetTime: \"2026-03-24T11:15:00Z\"\n",
  "restoreStatus": {
    "phase": "bootstrapping",
    "phaseReason": "create accepted",
    "message": "restore cluster resource created",
    "timestamps": {
      "bootstrappingStartedAt": "2026-03-24T12:31:00Z",
      "lastTransitionAt": "2026-03-24T12:31:00Z"
    }
  }
}
```

### Common error responses

| Status | When it happens | Example |
| --- | --- | --- |
| `400` | Missing `backupName`, invalid target identity, invalid JSON, unsupported restore method, or invalid/out-of-window `pitrTargetTime`. | `{"error":"backupName is required"}` |
| `404` | Source cluster is missing. | `{"error":"cluster \"missing\" in namespace \"team-a\" not found"}` |
| `409` | Backup is incomplete, the target already exists, the source does not advertise PITR data, or the creator reports a conflict. | `{"error":"cluster \"alpha-restore\" in namespace \"team-b\" already exists"}` |
| `503` | No restore creator was wired into the API server. | `{"error":"restore creation is not configured"}` |

## `GET /api/clusters/{namespace}/{name}/restore-status`

Returns the current guided-restore progress for a target cluster.

### Phase model

| Phase | Meaning |
| --- | --- |
| `bootstrapping` | Target cluster resource exists, but recovery has not advanced far enough to count as recovering. |
| `recovering` | The cluster shows restore/recovery signals but is not ready yet. |
| `ready` | Ready condition or equivalent cluster status indicates recovery completed. |
| `failed` | Failure markers or a failing ready condition indicate restore failure. |

### Example ready response

```json
{
  "cluster": {
    "namespace": "team-b",
    "name": "alpha-restore",
    "desiredInstances": 3,
    "readyInstances": 3
  },
  "status": {
    "phase": "ready",
    "phaseReason": "ready",
    "message": "cluster is ready",
    "timestamps": {
      "bootstrappingStartedAt": "2026-03-24T12:00:00Z",
      "recoveringStartedAt": "2026-03-24T12:05:00Z",
      "readyAt": "2026-03-24T12:10:00Z",
      "lastTransitionAt": "2026-03-24T12:10:00Z"
    }
  }
}
```

### Example failed response

```json
{
  "cluster": {
    "namespace": "team-b",
    "name": "alpha-restore"
  },
  "status": {
    "phase": "failed",
    "phaseReason": "restore error",
    "message": "restore job failed against <redacted>:9187",
    "error": "restore job failed against <redacted>:9187",
    "timestamps": {
      "bootstrappingStartedAt": "2026-03-24T12:00:00Z",
      "failedAt": "2026-03-24T12:20:00Z",
      "lastTransitionAt": "2026-03-24T12:20:00Z"
    }
  }
}
```

## `GET /ws`

Upgrades the connection to WebSocket and streams invalidation events whenever the in-memory store changes. The frontend treats these messages as hints and refetches the relevant REST endpoints instead of trusting optimistic client state.

### Event shape

```json
{
  "type": "store.changed",
  "kind": "cluster",
  "action": "upsert",
  "namespace": "team-a",
  "name": "alpha",
  "occurredAt": "2026-03-24T12:34:56Z"
}
```

### Notes

- The hub sends pings and drops slow clients instead of buffering unbounded history.
- The message only carries change metadata; full object payloads stay on the REST endpoints.

## Related docs

- [README](../README.md)
- [Architecture overview](architecture.md)
- [Permissions and RBAC](permissions.md)
- [Helm chart README](../charts/deckhand/README.md)
