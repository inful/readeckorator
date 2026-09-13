# syntax=docker/dockerfile:1.6
#
# Multi-stage Dockerfile for readeckorator.
#
# Stage 1: build a fully static Linux binary using the project's
# pinned Go toolchain. CGO is disabled because we use the pure-Go
# modernc.org/sqlite driver (no glibc/libmusl required).
#
# Stage 2: distroless/static:nonroot — no shell, no package manager,
# runs as uid 65532. The image is ~15 MB on amd64.
#
# Usage:
#   docker build -t readeckorator:dev .
#   docker run --rm -v $HOME/.local/share/readeckorator:/data \
#     -e READECK_API_TOKEN=... -e LLM_API_KEY=... \
#     readeckorator:dev --config /config/readeckorator.yaml run
#
# In production, goreleaser builds the image from .goreleaser.yaml
# and pushes a multi-arch manifest to ghcr.io/inful/readeckorator.

FROM golang:1.26-alpine AS build

WORKDIR /src

# Cache go.mod/go.sum first so dependency download only runs when
# they change — this makes iterative builds much faster.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# Build flags:
#   -trimpath         remove absolute paths from the binary (reproducibility)
#   -ldflags "-s -w"   strip symbol/debug info
#   -ldflags -X ...   inject the version string from $VERSION (set by goreleaser)
#   CGO_ENABLED=0     pure-Go, no glibc requirement
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/readeckorator ./cmd/readeckorator

# ---
FROM gcr.io/distroless/static:nonroot

COPY --from=build /out/readeckorator /usr/local/bin/readeckorator

USER nonroot:nonroot
WORKDIR /home/nonroot

ENTRYPOINT ["/usr/local/bin/readeckorator"]
CMD ["--help"]
