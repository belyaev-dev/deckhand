# Contributing to Deckhand

Thanks for helping improve Deckhand.

This repository is a mixed Go + React + Helm project. The canonical build and test entrypoints are the root `Makefile`, the Helm chart under `charts/deckhand/`, and the frontend package in `web/`.

## Toolchain

You should have the following installed locally:

- **Go 1.24.5** (from `go.mod`)
- **Node.js 18+** and npm (the Vite/Vitest toolchain requires a modern Node runtime)
- **Helm 3** for chart linting and template checks
- **Docker** if you want to build the container image locally
- **A working Kubernetes config** (`KUBECONFIG`) if you want to run the backend against a real cluster instead of relying on tests

## First-time setup

Install frontend dependencies once:

```bash
npm --prefix web ci
```

Build the shipped binary:

```bash
make build
```

`make build` runs `npm --prefix web run build` first so the frontend is embedded into the Go binary before compilation.

## Day-to-day workflow

### Run the full test suite

```bash
make test
```

That target covers:

- Go unit/integration-style tests via `go test ./...`
- frontend tests via `npm --prefix web run test -- --run`

### Frontend development

Run the Vite dev server:

```bash
npm --prefix web run dev
```

Vite listens on `127.0.0.1:5173` and proxies `/api` and `/ws` to `http://127.0.0.1:8080` by default. If your backend is somewhere else, set `DECKHAND_DEV_BACKEND` before starting Vite.

### Backend development

Run Deckhand locally against a kubeconfig:

```bash
go run ./cmd/deckhand --kubeconfig "$KUBECONFIG" --listen 127.0.0.1:8080
```

Useful runtime knobs:

- `--listen` / `DECKHAND_LISTEN` — bind address for the API and embedded SPA
- `--kubeconfig` / `KUBECONFIG` — kubeconfig path for local development
- `--namespaces` / `DECKHAND_NAMESPACES` — comma-separated namespace filter (`empty = all namespaces`)

If neither `--kubeconfig` nor in-cluster config is available, the backend exits during Kubernetes bootstrap.

### Build the container image

```bash
make docker-build
```

### Helm checks

Lint or render the chart directly:

```bash
make helm-lint
make helm-template
bash scripts/verify-helm-chart.sh
```

## What to verify before opening a PR

At minimum, run:

```bash
make build
make test
```

Then run any workflow-specific checks that match your change, such as:

- `bash scripts/verify-helm-chart.sh` for chart/RBAC changes
- a local UI smoke test when changing routed pages, browser state, or embedded assets
- API-specific checks when editing handlers or DTOs under `internal/api/`

## Pull request guidance

Please keep PRs grounded in the repository’s current contracts:

- Reuse the existing `Makefile` targets in docs and automation instead of inventing parallel commands.
- Keep REST and WebSocket docs aligned with `internal/api/server.go` and `internal/api/types.go`.
- Keep Helm install and RBAC guidance aligned with `charts/deckhand/README.md` and the chart templates.
- Add or update tests whenever behavior changes; do not rely on screenshots or prose alone.
- Do not commit secrets, kubeconfigs, tokens, or cluster-specific credentials.

## Repo map

```text
cmd/deckhand/      runtime entrypoint
internal/api/      HTTP handlers and DTOs
internal/k8s/      Kubernetes bootstrap and mutation logic
internal/metrics/  metrics scraping and health thresholds
internal/store/    in-memory state and change events
web/               React SPA
charts/deckhand/   Helm chart
```

If you are changing how Deckhand is installed, run, or described publicly, please update the relevant docs in the repo root or `docs/` as part of the same PR.
