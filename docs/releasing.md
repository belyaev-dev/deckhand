# Releasing Deckhand

Deckhand uses a tag-driven release flow. Pushing a semver tag to `main` triggers the [release workflow](../.github/workflows/release.yml), which builds the binary, pushes a Docker image to GHCR, and creates a GitHub Release.

## Prerequisites

- All CI checks pass on `main` (`make test`, `go vet`, TypeScript type-check, Docker build)
- You have push access to the repository

## Step-by-step

### 1. Verify `main` is green

Check the latest CI run on the [Actions tab](../../actions/workflows/ci.yml) or run locally:

```bash
make test
go vet ./...
make docker-build
```

### 2. Choose a version

Follow [semver](https://semver.org/):

| Change | Bump |
|--------|------|
| Breaking API or Helm changes | major (`v2.0.0`) |
| New feature, backward-compatible | minor (`v0.2.0`) |
| Bug fix or docs-only | patch (`v0.1.1`) |

### 3. Tag and push

```bash
git tag v0.1.0
git push origin v0.1.0
```

This triggers the `Release` workflow.

### 4. What the workflow produces

| Artifact | Location |
|----------|----------|
| GitHub Release | `github.com/<owner>/deckhand/releases/tag/v0.1.0` |
| Release notes | Auto-generated from commits since the last tag |
| `deckhand` binary | Attached to the GitHub Release |
| Docker image | `ghcr.io/<owner>/deckhand:0.1.0` |
| Docker image (minor) | `ghcr.io/<owner>/deckhand:0.1` |
| Docker image (sha) | `ghcr.io/<owner>/deckhand:sha-<commit>` |

### 5. Verify the release

```bash
# Check the GitHub Release page
gh release view v0.1.0

# Pull and inspect the image
docker pull ghcr.io/<owner>/deckhand:0.1.0
docker run --rm ghcr.io/<owner>/deckhand:0.1.0 --help
```

## Fixing a bad release

If a release has a critical issue:

1. Delete the tag and release:

   ```bash
   git tag -d v0.1.0
   git push origin :refs/tags/v0.1.0
   gh release delete v0.1.0 --yes
   ```

2. Fix the issue on `main`.

3. Re-tag with the same version or bump to a patch release, then push the tag again.

## Image tagging strategy

The release workflow uses [docker/metadata-action](https://github.com/docker/metadata-action) to produce three tags per release:

- `{{version}}` — full semver, e.g. `0.1.0`
- `{{major}}.{{minor}}` — minor track, e.g. `0.1`
- `sha-{{sha}}` — commit SHA for traceability

There is no `latest` tag. Users should pin to a specific version.
