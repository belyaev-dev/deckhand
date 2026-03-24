BINARY ?= deckhand
IMAGE ?= deckhand:test
CHART_DIR ?= charts/deckhand
HELM_RELEASE ?= deckhand
GO_BUILD_FLAGS ?= -trimpath

WEB_DEPS_STAMP := web/node_modules/.package-lock.stamp

.PHONY: build go-build test go-test web-build web-test docker-build helm-lint helm-template clean

build: go-build

go-build: web-build
	CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) -o $(BINARY) ./cmd/deckhand

test: web-build go-test web-test

go-test:
	go test ./...

web-build: $(WEB_DEPS_STAMP)
	npm --prefix web run build

web-test: $(WEB_DEPS_STAMP)
	npm --prefix web run test -- --run

$(WEB_DEPS_STAMP): web/package.json web/package-lock.json
	npm --prefix web ci
	@mkdir -p $(dir $@)
	@touch $@

docker-build:
	docker build -t $(IMAGE) .

helm-lint:
	@test -d $(CHART_DIR) || { echo "missing $(CHART_DIR); scaffold the Helm chart before linting" >&2; exit 1; }
	helm lint $(CHART_DIR)

helm-template:
	@test -d $(CHART_DIR) || { echo "missing $(CHART_DIR); scaffold the Helm chart before templating" >&2; exit 1; }
	helm template $(HELM_RELEASE) $(CHART_DIR)

clean:
	rm -f $(BINARY)
