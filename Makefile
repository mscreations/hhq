SHELL := /bin/bash

MODULE       := github.com/mscreations/hhq
BINARY       := hhq
BUILD_DIR    := bin
IMAGE        ?= ghcr.io/mscreations/hhq
TAG          ?= latest
DOCKERFILE   := deploy/Dockerfile
COVER_FILE   := coverage.out
VERSION      ?= dev

HTMX_MIN_AGE_DAYS ?= 90

.PHONY: help build run test test-verbose coverage coverage-html vet fmt tidy \
        docker-build docker-run docker-push clean htmx-check htmx-update htmx-diff

help:
	@echo "Targets:"
	@echo "  build          Build the server binary into $(BUILD_DIR)/$(BINARY)"
	@echo "  run            Build and run the server locally (reads .env)"
	@echo "  test           Run go test ./... (DB/SMTP-backed tests need Docker; they skip without it)"
	@echo "  test-verbose   Run tests with -v"
	@echo "  coverage       Run tests with coverage, print summary"
	@echo "  coverage-html  Run tests with coverage, open HTML report"
	@echo "  vet            Run go vet ./..."
	@echo "  fmt            Run gofmt -l on the tree (lists unformatted files)"
	@echo "  tidy           Run go mod tidy"
	@echo "  docker-build   Build the container image (context = repo root, not deploy/)"
	@echo "  docker-run     Run the container image locally, env from .env"
	@echo "  docker-push    Push the container image"
	@echo "  htmx-check     Report current vs. latest htmx release, no changes"
	@echo "  htmx-update    Vendor the latest htmx release, but only if it's"
	@echo "                 newer AND at least HTMX_MIN_AGE_DAYS old (default 90)"
	@echo "  htmx-diff      Prettify current vs. latest htmx release and diff them"
	@echo "                 (requires: pip install jsbeautifier)"
	@echo "  clean          Remove build artifacts"

build:
	go build -ldflags="-X main.Version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY) ./cmd/server

run: build
	@set -a; [ -f .env ] && . ./.env; set +a; ./$(BUILD_DIR)/$(BINARY)

test:
	go test ./...

test-verbose:
	go test -v ./...

coverage:
	go test -coverprofile=$(COVER_FILE) -coverpkg=./... $$(go list ./... | grep -v /internal/testutil)
	go tool cover -func=$(COVER_FILE)

coverage-html: coverage
	go tool cover -html=$(COVER_FILE)

vet:
	go vet ./...

fmt:
	gofmt -l .

tidy:
	go mod tidy

# The Dockerfile COPYs go.mod/go.sum and the full source tree, so the build
# context must be the repo root, not deploy/ - `docker build deploy/` (or
# `.\deploy\` on Windows) fails because go.mod isn't visible in that context.
docker-build:
	docker build --build-arg VERSION=$(VERSION) -f $(DOCKERFILE) -t $(IMAGE):$(TAG) .

docker-run: docker-build
	docker run --rm -p 8080:8080 --env-file .env $(IMAGE):$(TAG)

docker-push:
	docker push $(IMAGE):$(TAG)

# On Windows, prefer the native pwsh script (no jq/GNU-diff/mktemp/date -d
# dependency); everywhere else use the bash script. $OS=Windows_NT is set by
# Windows itself and is visible from bash (incl. Git Bash), so this dispatch
# works no matter which shell invoked `make`.
HTMX_USE_PWSH := $(shell [ "$$OS" = "Windows_NT" ] && command -v pwsh >/dev/null 2>&1 && echo yes)

htmx-check:
ifeq ($(HTMX_USE_PWSH),yes)
	@pwsh -NoProfile -File scripts/Update-Htmx.ps1 -Mode Check -MinAgeDays $(HTMX_MIN_AGE_DAYS)
else
	@HTMX_MIN_AGE_DAYS=$(HTMX_MIN_AGE_DAYS) ./scripts/update-htmx.sh check
endif

htmx-update:
ifeq ($(HTMX_USE_PWSH),yes)
	@pwsh -NoProfile -File scripts/Update-Htmx.ps1 -Mode Update -MinAgeDays $(HTMX_MIN_AGE_DAYS)
else
	@HTMX_MIN_AGE_DAYS=$(HTMX_MIN_AGE_DAYS) ./scripts/update-htmx.sh update
endif

htmx-diff:
ifeq ($(HTMX_USE_PWSH),yes)
	@pwsh -NoProfile -File scripts/Update-Htmx.ps1 -Mode Diff
else
	@./scripts/update-htmx.sh diff
endif

clean:
	rm -rf $(BUILD_DIR) $(COVER_FILE)
