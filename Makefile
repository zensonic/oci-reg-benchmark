# Copyright The ORB Authors
# SPDX-License-Identifier: Apache-2.0

# Common developer tasks. CI runs the same targets, so "make ci" locally
# gives the same answer as the pipeline.

GO      ?= go
MODULE  := github.com/zensonic/oci-reg-benchmark
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
IMAGE   ?= ghcr.io/zensonic/oci-reg-benchmark/orb:$(VERSION)

LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)

# Pinned tool versions. Dependabot does not update these, so bump them by hand.
GOLANGCI_LINT_VERSION := v2.15.0
ADDLICENSE_VERSION    := v1.2.0
GO_LICENSES_VERSION   := v2.0.1
CONTROLLER_GEN_VERSION := v0.23.0
CONTROLLER_GEN := $(GO) run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

# Third-party licenses allowed without an exception by the CNCF
# allowlist license policy (see requirements.md, LIC-5).
# Source: https://github.com/cncf/foundation/blob/main/policies-guidance/allowed-third-party-license-policy.md
ALLOWED_LICENSES := Apache-2.0,0BSD,BSD-2-Clause,BSD-2-Clause-FreeBSD,BSD-3-Clause,MIT,MIT-0,ISC,OpenSSL,PSF-2.0,Python-2.0,Python-2.0.1,PostgreSQL,UPL-1.0,X11,Zlib

.PHONY: all build generate generate-check test lint tidy-check license-headers license-headers-fix dependency-licenses image ci clean

all: build

## build: compile orb and orbctl into ./bin
# orb needs CGO for the SQLite driver (RES-8), so a C compiler must be
# installed. orbctl does not use SQLite and is built without CGO.
build:
	CGO_ENABLED=1 $(GO) build -trimpath -tags sqlite_omit_load_extension -ldflags "$(LDFLAGS)" -o bin/orb ./cmd/orb
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/orbctl ./cmd/orbctl

## generate: regenerate deepcopy code and the CRD manifests from api/v1alpha1
generate:
	$(CONTROLLER_GEN) object paths=./api/...
	$(CONTROLLER_GEN) crd:allowDangerousTypes=true paths=./api/... output:crd:dir=deploy/helm/orb/crds

## generate-check: fail if generated files are out of date
generate-check: generate
	git diff --exit-code -- api deploy/helm/orb/crds

## test: run unit tests with the race detector
test:
	$(GO) test -race -coverprofile=coverage.out ./...

## lint: run golangci-lint
lint:
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

## tidy-check: fail if go.mod or go.sum are not tidy
tidy-check:
	$(GO) mod tidy -diff

## license-headers: fail if a source file lacks the license header (LIC-3)
license-headers:
	$(GO) run github.com/google/addlicense@$(ADDLICENSE_VERSION) -check \
		-ignore 'dist/**' -ignore 'bin/**' -ignore '**/*.yaml' -ignore '**/*.yml' \
		.

## license-headers-fix: add the license header to files that lack it
license-headers-fix:
	$(GO) run github.com/google/addlicense@$(ADDLICENSE_VERSION) \
		-s=only -c "The ORB Authors" -l apache -y "" \
		-ignore 'dist/**' -ignore 'bin/**' -ignore '**/*.yaml' -ignore '**/*.yml' \
		.

## dependency-licenses: fail if a dependency uses a license outside the CNCF allowlist (LIC-5)
dependency-licenses:
	$(GO) run github.com/google/go-licenses/v2@$(GO_LICENSES_VERSION) check ./... \
		--allowed_licenses=$(ALLOWED_LICENSES)

## image: build the orb container image for the local platform
image:
	docker build \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) \
		-t $(IMAGE) .

## ci: run the same checks as the CI pipeline
ci: tidy-check license-headers dependency-licenses lint test build

## clean: remove build output
clean:
	rm -rf bin dist coverage.out
