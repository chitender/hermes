# ── Stage 1: Build ────────────────────────────────────────────────────────────
# BuildKit automatically populates TARGETPLATFORM, TARGETOS, TARGETARCH, and
# TARGETVARIANT when `docker buildx build --platform ...` is used.
# Declaring them as ARGs exposes them inside the build stage.
FROM --platform=$BUILDPLATFORM golang:1.22-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /workspace

# Download dependencies first so Docker can cache this layer independently
# from source changes. This layer is shared across all target platforms
# because we run it on the build host (BUILDPLATFORM).
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod \
    go mod download

# Copy source
COPY cmd/       cmd/
COPY api/       api/
COPY internal/  internal/

# Cross-compile for the requested target platform.
# CGO_ENABLED=0 produces a fully static binary — required for distroless.
# GOARM is set from TARGETVARIANT (e.g. "v7" → GOARM=7) for arm/v7 builds.
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOARM=$(echo "${TARGETVARIANT}" | sed 's/v//') \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -ldflags="-s -w" \
      -trimpath \
      -o /workspace/manager \
      ./cmd/

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
# gcr.io/distroless/static:nonroot contains:
#   - CA certificates (for HTTPS to Vault / etcd)
#   - /etc/passwd with the "nonroot" user (UID 65532)
#   - No shell, no package manager — minimal attack surface
FROM gcr.io/distroless/static:nonroot

WORKDIR /

# Copy the compiled binary from the builder stage.
COPY --from=builder /workspace/manager /manager

# Run as the non-root user supplied by the distroless image.
# UID 65532 matches the "nonroot" account in the distroless image.
USER 65532:65532

ENTRYPOINT ["/manager"]
