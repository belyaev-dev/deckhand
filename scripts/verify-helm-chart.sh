#!/usr/bin/env bash
set -euo pipefail

chart_dir="charts/deckhand"
tmp_dir="tmp"
default_render="$tmp_dir/helm-default.yaml"
scoped_render="$tmp_dir/helm-scoped.yaml"
invalid_err="$tmp_dir/helm-template-namespace.err"

fail() {
  echo "verify-helm-chart: $*" >&2
  exit 1
}

assert_contains() {
  local file="$1"
  local pattern="$2"
  local message="$3"
  if ! rg -q --multiline "$pattern" "$file"; then
    fail "$message ($file)"
  fi
}

assert_not_contains() {
  local file="$1"
  local pattern="$2"
  local message="$3"
  if rg -q --multiline "$pattern" "$file"; then
    fail "$message ($file)"
  fi
}

assert_count() {
  local file="$1"
  local pattern="$2"
  local expected="$3"
  local description="$4"
  local actual
  actual=$(rg -c --multiline "$pattern" "$file" || true)
  if [[ -z "$actual" ]]; then
    actual=0
  fi
  if [[ "$actual" != "$expected" ]]; then
    fail "$description expected $expected, got $actual ($file)"
  fi
}

mkdir -p "$tmp_dir"

echo "==> helm lint"
helm lint "$chart_dir"

echo "==> render default cluster-wide chart"
helm template deckhand "$chart_dir" >"$default_render"

assert_count "$default_render" '^kind: ClusterRole$' 1 'cluster-wide render ClusterRole count'
assert_count "$default_render" '^kind: ClusterRoleBinding$' 1 'cluster-wide render ClusterRoleBinding count'
assert_count "$default_render" '^kind: Role$' 0 'cluster-wide render Role count'
assert_count "$default_render" '^kind: RoleBinding$' 0 'cluster-wide render RoleBinding count'
assert_contains "$default_render" 'resources:\n      - clusters\n    verbs:\n      - get\n      - list\n      - watch\n      - create' 'cluster-wide render missing clusters get/list/watch/create rule'
assert_contains "$default_render" 'resources:\n      - backups\n    verbs:\n      - get\n      - list\n      - watch\n      - create' 'cluster-wide render missing backups get/list/watch/create rule'
assert_contains "$default_render" 'resources:\n      - scheduledbackups\n    verbs:\n      - get\n      - list\n      - watch' 'cluster-wide render missing scheduledbackups get/list/watch rule'
assert_contains "$default_render" 'resources:\n      - pods\n      - persistentvolumeclaims\n    verbs:\n      - get\n      - list\n      - watch' 'cluster-wide render missing pods/PVC get/list/watch rule'
assert_not_contains "$default_render" 'resources:\n      - scheduledbackups\n    verbs:\n(?:      - .*\n)*      - create' 'cluster-wide render should not grant create on scheduledbackups'
assert_not_contains "$default_render" 'resources:\n      - pods\n      - persistentvolumeclaims\n    verbs:\n(?:      - .*\n)*      - create' 'cluster-wide render should not grant create on pods/PVCs'

echo "==> render namespace-scoped chart"
helm template deckhand "$chart_dir" --set rbac.clusterWide=false --set 'rbac.namespaces={production,staging}' >"$scoped_render"

assert_count "$scoped_render" '^kind: ClusterRole$' 0 'namespace-scoped render ClusterRole count'
assert_count "$scoped_render" '^kind: ClusterRoleBinding$' 0 'namespace-scoped render ClusterRoleBinding count'
assert_count "$scoped_render" '^kind: Role$' 2 'namespace-scoped render Role count'
assert_count "$scoped_render" '^kind: RoleBinding$' 2 'namespace-scoped render RoleBinding count'
assert_contains "$scoped_render" 'kind: Role\nmetadata:\n  name: deckhand\n  namespace: production' 'namespace-scoped render missing production Role'
assert_contains "$scoped_render" 'kind: Role\nmetadata:\n  name: deckhand\n  namespace: staging' 'namespace-scoped render missing staging Role'
assert_contains "$scoped_render" 'kind: RoleBinding\nmetadata:\n  name: deckhand\n  namespace: production' 'namespace-scoped render missing production RoleBinding'
assert_contains "$scoped_render" 'kind: RoleBinding\nmetadata:\n  name: deckhand\n  namespace: staging' 'namespace-scoped render missing staging RoleBinding'
assert_contains "$scoped_render" 'resources:\n      - clusters\n    verbs:\n      - get\n      - list\n      - watch\n      - create' 'namespace-scoped render missing clusters get/list/watch/create rule'
assert_contains "$scoped_render" 'resources:\n      - backups\n    verbs:\n      - get\n      - list\n      - watch\n      - create' 'namespace-scoped render missing backups get/list/watch/create rule'
assert_contains "$scoped_render" 'resources:\n      - scheduledbackups\n    verbs:\n      - get\n      - list\n      - watch' 'namespace-scoped render missing scheduledbackups get/list/watch rule'
assert_contains "$scoped_render" 'resources:\n      - pods\n      - persistentvolumeclaims\n    verbs:\n      - get\n      - list\n      - watch' 'namespace-scoped render missing pods/PVC get/list/watch rule'
assert_not_contains "$scoped_render" 'resources:\n      - scheduledbackups\n    verbs:\n(?:      - .*\n)*      - create' 'namespace-scoped render should not grant create on scheduledbackups'
assert_not_contains "$scoped_render" 'resources:\n      - pods\n      - persistentvolumeclaims\n    verbs:\n(?:      - .*\n)*      - create' 'namespace-scoped render should not grant create on pods/PVCs'

echo "==> verify namespace validation failure"
if helm template deckhand "$chart_dir" --set rbac.scope=namespace >/dev/null 2>"$invalid_err"; then
  fail 'expected namespace-scoped render without namespaces to fail'
fi
assert_contains "$invalid_err" 'namespace-scoped installs require at least one namespace' 'namespace-scoped validation error did not mention missing namespaces'

echo "verify-helm-chart: all checks passed"
