#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"

checks=(
  "required-files:required docs and launch assets exist"
  "quickstart-commands:README, CONTRIBUTING, Makefile, and chart quick-start commands stay aligned"
  "api-doc-coverage:every shipped REST and WebSocket path is documented in docs/api.md"
  "rbac-matrix:docs/permissions.md matches the shipped Helm RBAC contract"
  "markdown-links:published markdown links and repo-local asset references resolve"
)

fail() {
  local check="$1"
  shift
  echo "verify-docs [$check]: $*" >&2
  exit 1
}

assert_file_nonempty() {
  local check="$1"
  local rel="$2"
  [[ -s "$repo_root/$rel" ]] || fail "$check" "missing or empty $rel"
}

assert_contains() {
  local check="$1"
  local rel="$2"
  local pattern="$3"
  local message="$4"
  if ! rg -q --multiline -- "$pattern" "$repo_root/$rel"; then
    fail "$check" "$message ($rel)"
  fi
}

list_checks() {
  printf '%s\n' "${checks[@]}"
}

check_required_files() {
  local check="required-files"
  local required=(
    "README.md"
    "CONTRIBUTING.md"
    "LICENSE"
    "docs/architecture.md"
    "docs/api.md"
    "docs/permissions.md"
    "docs/screenshots/overview.png"
    "docs/screenshots/restore.png"
    "docs/demo/deckhand-demo.gif"
    "scripts/verify-helm-chart.sh"
  )

  local rel
  for rel in "${required[@]}"; do
    assert_file_nonempty "$check" "$rel"
  done
}

check_quickstart_commands() {
  local check="quickstart-commands"

  assert_contains "$check" "README.md" 'helm install deckhand charts/deckhand' 'README is missing the cluster-wide Helm install command'
  assert_contains "$check" "README.md" '--set rbac\.clusterWide=false' 'README is missing namespace-scoped Helm values'
  assert_contains "$check" "README.md" 'docs/api\.md' 'README is missing the API reference link'
  assert_contains "$check" "README.md" 'docs/permissions\.md' 'README is missing the permissions reference link'

  assert_contains "$check" "charts/deckhand/README.md" 'helm install deckhand charts/deckhand' 'chart README is missing the cluster-wide Helm install command'
  assert_contains "$check" "charts/deckhand/README.md" '--set rbac\.clusterWide=false' 'chart README is missing namespace-scoped Helm values'
  assert_contains "$check" "charts/deckhand/README.md" 'namespace-scoped installs require at least one namespace in rbac\.namespaces' 'chart README is missing the namespace validation failure message'

  assert_contains "$check" "CONTRIBUTING.md" 'make build' 'CONTRIBUTING is missing make build'
  assert_contains "$check" "CONTRIBUTING.md" 'make test' 'CONTRIBUTING is missing make test'
  assert_contains "$check" "CONTRIBUTING.md" 'bash scripts/verify-helm-chart\.sh' 'CONTRIBUTING is missing the chart verification command'

  assert_contains "$check" "Makefile" '^build:' 'Makefile is missing the build target referenced by the docs'
  assert_contains "$check" "Makefile" '^test:' 'Makefile is missing the test target referenced by the docs'
  assert_contains "$check" "Makefile" '^helm-lint:' 'Makefile is missing the helm-lint target referenced by the docs'
  assert_contains "$check" "Makefile" '^helm-template:' 'Makefile is missing the helm-template target referenced by the docs'
}

check_api_doc_coverage() {
  local check="api-doc-coverage"

  python3 - "$repo_root" <<'PY' || fail "$check" "docs/api.md is missing one or more shipped API paths"
from pathlib import Path
import re
import sys

repo_root = Path(sys.argv[1])
server = (repo_root / "internal/api/server.go").read_text()
docs = (repo_root / "docs/api.md").read_text()

def normalize(path: str) -> str:
    if path == "/api/":
        return "/api"
    return path.rstrip("/") or "/"

routes = []
for method, path in re.findall(r'\br\.(Get|Post)\("([^"]+)"', server):
    if path.startswith("/api") or path in {"/healthz", "/ws"}:
        routes.append((method.upper(), normalize(path)))

api_block = re.search(r'r\.Route\("/api", func\(r chi\.Router\) \{(.*?)\n\t\}\)', server, re.S)
if not api_block:
    raise SystemExit("missing /api route block")
for method, path in re.findall(r'\br\.(Get|Post)\("([^"]+)"', api_block.group(1)):
    full = "/api" if path == "/" else f"/api{path}"
    routes.append((method.upper(), normalize(full)))

missing = []
for method, path in sorted(set(routes)):
    needle = f"{method} {path}"
    if needle not in docs:
        missing.append(needle)

extra_required = [
    "store.changed",
    "route not found",
    "bootstrapping",
    "recovering",
    "ready",
    "failed",
]
for needle in extra_required:
    if needle not in docs:
        missing.append(needle)

if missing:
    sys.stderr.write("missing docs coverage for: " + ", ".join(missing) + "\n")
    raise SystemExit(1)
PY
}

check_rbac_matrix() {
  local check="rbac-matrix"

  assert_contains "$check" "docs/permissions.md" 'ClusterRole' 'permissions doc is missing ClusterRole coverage'
  assert_contains "$check" "docs/permissions.md" 'ClusterRoleBinding' 'permissions doc is missing ClusterRoleBinding coverage'
  assert_contains "$check" "docs/permissions.md" 'Role' 'permissions doc is missing Role coverage'
  assert_contains "$check" "docs/permissions.md" 'RoleBinding' 'permissions doc is missing RoleBinding coverage'
  assert_contains "$check" "docs/permissions.md" 'DECKHAND_NAMESPACES' 'permissions doc is missing DECKHAND_NAMESPACES behavior'
  assert_contains "$check" "docs/permissions.md" 'namespace-scoped installs require at least one namespace in rbac\.namespaces' 'permissions doc is missing the namespace validation failure message'
  assert_contains "$check" "docs/permissions.md" 'clusters.*get.*list.*watch.*create' 'permissions doc is missing the clusters get/list/watch/create rule'
  assert_contains "$check" "docs/permissions.md" 'backups.*get.*list.*watch.*create' 'permissions doc is missing the backups get/list/watch/create rule'
  assert_contains "$check" "docs/permissions.md" 'scheduledbackups.*get.*list.*watch' 'permissions doc is missing the scheduledbackups get/list/watch rule'
  assert_contains "$check" "docs/permissions.md" 'pods.*, `persistentvolumeclaims`.*get.*list.*watch' 'permissions doc is missing the pods/PVC get/list/watch rule'
}

check_markdown_links() {
  local check="markdown-links"

  python3 - "$repo_root" <<'PY' || fail "$check" "one or more markdown links are broken"
from pathlib import Path
import re
import sys
import unicodedata

repo_root = Path(sys.argv[1])
files = [
    repo_root / "README.md",
    repo_root / "CONTRIBUTING.md",
    repo_root / "docs/architecture.md",
    repo_root / "docs/api.md",
    repo_root / "docs/permissions.md",
    repo_root / "charts/deckhand/README.md",
]

link_re = re.compile(r'!?\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)')
heading_re = re.compile(r'^(#{1,6})\s+(.*)$', re.M)


def slugify(value: str) -> str:
    value = value.strip().lower()
    value = unicodedata.normalize("NFKD", value)
    value = "".join(ch for ch in value if not unicodedata.combining(ch))
    value = re.sub(r'[`*_~\[\](){}.!?,:;"\'/\\]', '', value)
    value = re.sub(r'\s+', '-', value)
    value = re.sub(r'-+', '-', value)
    return value.strip('-')

anchors = {}
for file in files:
    text = file.read_text()
    anchors[file] = {slugify(match.group(2)) for match in heading_re.finditer(text)}

errors = []
for file in files:
    text = file.read_text()
    for target in link_re.findall(text):
        if target.startswith(("http://", "https://", "mailto:", "#")):
            if target.startswith("#"):
                anchor = slugify(target[1:])
                if anchor and anchor not in anchors[file]:
                    errors.append(f"{file.relative_to(repo_root)} -> missing anchor #{target[1:]}")
            continue
        if target.startswith("data:"):
            continue
        path_part, _, anchor_part = target.partition('#')
        if path_part.startswith('/'):
            resolved = repo_root / path_part.lstrip('/')
        else:
            resolved = (file.parent / path_part).resolve()
        if not resolved.exists():
            errors.append(f"{file.relative_to(repo_root)} -> missing path {target}")
            continue
        if anchor_part:
            file_anchors = anchors.get(resolved)
            if file_anchors is None and resolved.suffix.lower() == '.md':
                file_anchors = {slugify(match.group(2)) for match in heading_re.finditer(resolved.read_text())}
            if resolved.suffix.lower() == '.md':
                anchor = slugify(anchor_part)
                if anchor and anchor not in file_anchors:
                    errors.append(f"{file.relative_to(repo_root)} -> missing anchor {target}")

if errors:
    sys.stderr.write("broken markdown links:\n" + "\n".join(errors) + "\n")
    raise SystemExit(1)
PY
}

run_check() {
  local entry="$1"
  local name="${entry%%:*}"
  echo "==> $name"
  "check_${name//-/_}"
}

main() {
  if [[ "${1:-}" == "--list-checks" ]]; then
    list_checks
    return 0
  fi

  if [[ "${1:-}" == "--help" ]]; then
    cat <<'EOF'
Usage: bash scripts/verify-docs.sh [--list-checks]

Verifies launch-doc completeness against the shipped docs, assets, API routes, RBAC matrix, and internal markdown links.
EOF
    return 0
  fi

  local entry
  for entry in "${checks[@]}"; do
    run_check "$entry"
  done

  echo "verify-docs: all checks passed"
}

main "$@"
