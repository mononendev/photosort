REGISTRY  := registry.adoah.dev/projects
API_IMAGE := $(REGISTRY)/photosort-api
UI_IMAGE  := $(REGISTRY)/photosort-ui
PLATFORM  := linux/amd64
BUILDER   ?= homelab-remote-builder

GIT_SHA    := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_ARGS = --build-arg VERSION=$(VERSION) --build-arg GIT_SHA=$(GIT_SHA) --build-arg BUILD_TIME=$(BUILD_TIME)

.PHONY: dev web test build-api build-ui push deploy

dev:               ## run API locally against dev-data
	.venv/bin/python -m photosort --workdir photosort_work web --photos dev-data --port 8080

web:               ## run the Vite dev server (proxies /api to :8080)
	cd web && pnpm install && pnpm run dev

test:
	.venv/bin/python -m pytest -q tests
	cd web && pnpm run build && pnpm run lint

build-api:
	docker buildx build --builder $(BUILDER) --platform $(PLATFORM) --file docker/api.Dockerfile --target production \
		$(VERSION_ARGS) --tag $(API_IMAGE):$(VERSION) --tag $(API_IMAGE):latest --push .

build-ui:
	docker buildx build --builder $(BUILDER) --platform $(PLATFORM) --file docker/ui.Dockerfile --target production \
		$(VERSION_ARGS) --tag $(UI_IMAGE):$(VERSION) --tag $(UI_IMAGE):latest --push .

push: build-api build-ui

deploy:            ## helm upgrade into production with the current VERSION tag
	helm repo add mononen-charts https://mononen.github.io/charts/ >/dev/null 2>&1 || true
	helm repo update mononen-charts >/dev/null
	helm dependency update .ci/chart
	helm upgrade --install photosort .ci/chart --namespace production \
		--set mononen-library-chart.global.imageDefaults.tag=$(VERSION)
