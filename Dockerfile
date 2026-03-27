# ── Stage 1: Build ────────────────────────────────────────────────────────────
# Built natively on each target machine (amd64 or arm64).
# Go automatically targets the host platform — no GOARCH needed.
FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

# Download dependencies first so Docker can cache this layer independently
# from source changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod \
    go mod download

# Copy source
COPY cmd/       cmd/
COPY api/       api/
COPY internal/  internal/

# CGO_ENABLED=0 produces a fully static binary — required for distroless.
# GOOS=linux is explicit in case the image is ever built on a macOS runner.
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
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
