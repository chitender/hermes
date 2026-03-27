# ── Stage 1: Build ────────────────────────────────────────────────────────────
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

# Build the operator binary.
# CGO_ENABLED=0 produces a fully static binary compatible with distroless.
# -ldflags="-s -w" strips debug symbols, reducing image size.
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
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
