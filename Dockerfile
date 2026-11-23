# syntax=docker/dockerfile:1
# Copyright The ORB Authors
# SPDX-License-Identifier: Apache-2.0

# Builds the "orb" image, which runs both the controller and the agent
# (ENG-5).
#
# The SQLite driver (github.com/mattn/go-sqlite3, RES-8) needs CGO. To keep
# the build simple, the build stage runs on the target platform itself
# (emulated with QEMU for other architectures) instead of cross-compiling a
# C toolchain. The binary is linked statically (BUILD-7) so it runs on a
# distroless static base image.

FROM golang:1.27.4@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build

WORKDIR /src

# Download dependencies first so they are cached between builds.
# go.sum does not exist until the project has dependencies, hence the glob.
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

# Build tags:
#   netgo, osusergo            use Go's own DNS and user lookups, so static linking works
#   sqlite_omit_load_extension SQLite never loads shared libraries at runtime
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 \
    go build -trimpath \
      -tags "netgo osusergo sqlite_omit_load_extension" \
      -ldflags "-s -w -linkmode external -extldflags -static \
        -X github.com/zensonic/oci-reg-benchmark/internal/version.Version=${VERSION} \
        -X github.com/zensonic/oci-reg-benchmark/internal/version.Commit=${COMMIT} \
        -X github.com/zensonic/oci-reg-benchmark/internal/version.Date=${DATE}" \
      -o /out/orb ./cmd/orb

# Minimal runtime image: no shell, no package manager, runs as non-root.
FROM gcr.io/distroless/static:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931

COPY --from=build /out/orb /usr/local/bin/orb
COPY LICENSE /licenses/LICENSE

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/orb"]
