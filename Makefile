REGISTRY  := registry.adoah.dev/projects
PLATFORM  := linux/amd64
BUILDER   ?= homelab-remote-builder
IMAGES    := api analyzer ui

GIT_SHA    := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_ARGS = --build-arg VERSION=$(VERSION) --build-arg GIT_SHA=$(GIT_SHA) --build-arg BUILD_TIME=$(BUILD_TIME)
# Same registry cache CI reads and writes, so local and CI builds reuse each other's layers
CACHE_ARGS = --cache-from type=registry,ref=$(1):buildcache --cache-to type=registry,ref=$(1):buildcache,mode=max

PHOTOS  ?= dev-data
WORKDIR ?= photosort_work
export PHOTOSORT_MODELS ?= $(CURDIR)/models

.PHONY: dev web test analyzer models bin $(addprefix build-,$(IMAGES)) push deploy

dev:               ## API + job runner on :8080 against $(PHOTOS); starts the analyzer from analyzer/ itself
	go run ./cmd/photosort --workdir $(WORKDIR) web --photos $(PHOTOS) --port 8080

web:               ## the Vite dev server (proxies /api and /media to :8080)
	cd web && pnpm install && pnpm run dev

analyzer:          ## the analyzer's venv (add the ultralytics extra to convert YOLO models: make models)
	cd analyzer && uv sync

models:            ## install pose models into $(PHOTOSORT_MODELS)/pose, e.g. make models NAMES="yolo26s-pose rtmo-m"
	go run ./cmd/photosort models get $(or $(NAMES),yolo26s-pose)

bin:               ## bin/photosort
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o bin/photosort ./cmd/photosort

test:
	go vet ./... && go test -race ./...
	cd analyzer && uv run pytest -q
	cd web && pnpm run build && pnpm run lint

build-%:
	docker buildx build --builder $(BUILDER) --platform $(PLATFORM) --file docker/$*.Dockerfile --target production \
		$(VERSION_ARGS) $(call CACHE_ARGS,$(REGISTRY)/photosort-$*) \
		--tag $(REGISTRY)/photosort-$*:$(VERSION) --tag $(REGISTRY)/photosort-$*:latest --push .

push: $(addprefix build-,$(IMAGES))

deploy:            ## helm upgrade into production with the current VERSION tag
	helm repo add mononen-charts https://mononen.github.io/charts/ >/dev/null 2>&1 || true
	helm repo update mononen-charts >/dev/null
	helm dependency update .ci/chart
	helm upgrade --install photosort .ci/chart --namespace production \
		--set mononen-library-chart.global.imageDefaults.tag=$(VERSION)
