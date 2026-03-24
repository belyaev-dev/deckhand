# Deckhand permissions and RBAC

Deckhand's Helm chart ships with chart-managed least-privilege RBAC. The source of truth is the shared rule block in `charts/deckhand/templates/_helpers.tpl`, which is rendered either as cluster-scoped RBAC or as one namespace-scoped rule set per configured namespace.

## Install modes

### Cluster-wide install (default)

```bash
helm install deckhand charts/deckhand
```

This mode renders:

- one `ClusterRole`
- one `ClusterRoleBinding`
- a runtime `ConfigMap` without `DECKHAND_NAMESPACES`
- a single Deployment that watches all namespaces

Use this mode when Deckhand should discover every watched CloudNativePG cluster in the Kubernetes cluster.

### Namespace-scoped install

Preferred values:

```bash
helm install deckhand charts/deckhand \
  --set rbac.clusterWide=false \
  --set "rbac.namespaces={production,staging}"
```

Compatibility aliases are still accepted by the chart:

```bash
helm install deckhand charts/deckhand \
  --set rbac.scope=namespace \
  --set "namespaces={production,staging}"
```

This mode renders:

- one `Role` per configured namespace
- one `RoleBinding` per configured namespace
- no cluster-scoped RBAC resources
- `DECKHAND_NAMESPACES=production,staging` in the runtime `ConfigMap`

Render-time validation fails fast if namespace mode is selected without any namespaces:

```text
namespace-scoped installs require at least one namespace in rbac.namespaces
```

## Runtime identity

In both modes, Deckhand runs as the chart-managed ServiceAccount and binds permissions to that identity:

- cluster-wide mode binds the ServiceAccount through a `ClusterRoleBinding`
- namespace-scoped mode binds the same ServiceAccount through a `RoleBinding` in each watched namespace
- the `RoleBinding` subject still points at the release namespace ServiceAccount, so one Deckhand Deployment can watch multiple namespaces without creating one ServiceAccount per namespace

## Exact RBAC matrix

The chart renders four RBAC rules covering five concrete resources. The table below mirrors the helper template exactly.

| API group | Resource(s) | Verbs | Why Deckhand needs it |
| --- | --- | --- | --- |
| `postgresql.cnpg.io` | `clusters` | `get`, `list`, `watch`, `create` | Read watched CNPG clusters for overview/detail pages and create restore target `Cluster` resources. |
| `postgresql.cnpg.io` | `backups` | `get`, `list`, `watch`, `create` | Read backup history and create on-demand `Backup` resources. |
| `postgresql.cnpg.io` | `scheduledbackups` | `get`, `list`, `watch` | Surface scheduled backup policies in the detail and backups views. |
| core | `pods`, `persistentvolumeclaims` | `get`, `list`, `watch` | Resolve pod status/IP-backed metrics targets and PVC capacity for the metrics cache. |

## Least-privilege rationale

Deckhand intentionally does **not** ask for broad mutation rights.

### Why `create` exists only on `clusters` and `backups`

- `clusters:create` is required for the guided restore flow, which creates a brand-new CNPG `Cluster` resource for the restore target.
- `backups:create` is required for the on-demand backup action exposed in the UI.
- The current runtime does **not** need `update`, `patch`, or `delete` on those resources.

### Why `scheduledbackups` is read-only

Deckhand only documents and displays scheduled backup policy state today. It does not create, mutate, suspend, or delete `ScheduledBackup` objects, so the chart grants `get`, `list`, and `watch` only.

### Why Pods and PVCs are read-only

Deckhand's metrics layer scrapes exporter metrics per pod and reads PVC capacity information, but it never creates or mutates Pods or PersistentVolumeClaims. The chart therefore grants `get`, `list`, and `watch` only—never `create`, `update`, `patch`, or `delete`.

## Mode-to-resource mapping

| Install mode | RBAC resources rendered | Runtime namespace behavior |
| --- | --- | --- |
| cluster-wide | `ClusterRole` + `ClusterRoleBinding` | Watches all namespaces; `DECKHAND_NAMESPACES` is omitted. |
| namespace-scoped | one `Role` + one `RoleBinding` per configured namespace | Watches only the configured namespaces; `DECKHAND_NAMESPACES` is populated from `rbac.namespaces` (or the top-level `namespaces` compatibility alias). |

## What this means for operators

- If you need one Deckhand instance to observe the whole cluster, use the default cluster-wide mode.
- If you want the UI limited to specific namespaces, switch to namespace-scoped mode and enumerate them explicitly.
- The permission matrix is intentionally identical in both modes; only the scope of the bindings changes.
- Changes to UI/API features that start mutating `ScheduledBackup`, Pod, or PVC resources must also update the chart helpers and this document.

## Verification

Use the chart and docs verification gates together:

```bash
bash scripts/verify-helm-chart.sh
bash scripts/verify-docs.sh
```

## Related docs

- [README](../README.md)
- [Architecture overview](architecture.md)
- [API reference](api.md)
- [Helm chart README](../charts/deckhand/README.md)
