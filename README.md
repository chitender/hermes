# Hermes — Vault → etcd Sync Operator

Hermes is a Kubernetes Operator that continuously syncs secrets from **HashiCorp Vault KV v2** into **etcd** as rendered JSON, using a Go-template system.

```
Vault KV v2  ──►  Hermes Operator  ──►  etcd
  (secrets)        (Go template)       (rendered JSON)
```

## How it works

1. You create a `VaultEtcdSync` CR specifying which Vault paths to read, a Go template, and the etcd key to write to.
2. Hermes reads the secrets, merges them into a flat map, renders the template, validates the output is valid JSON, then writes it atomically to etcd.
3. A per-CR goroutine polls Vault's **metadata** endpoint to detect secret version changes and triggers an immediate re-sync — without reading actual secret values on every tick.
4. A periodic re-sync runs on `spec.syncInterval` as a fallback.

---

## Table of Contents

- [Quick Start](#quick-start)
- [Installation](#installation)
- [CRD Reference](docs/configuration.md)
- [Vault Authentication](docs/vault-auth.md)
- [Templating Guide](docs/templating.md)
- [Observability](docs/observability.md)
- [Examples](docs/examples/)

---

## Quick Start

```bash
# 1. Install CRD
kubectl apply -f config/crd/bases/sync.example.com_vaultedcsyncs.yaml

# 2. Create namespace
kubectl create namespace vault-etcd-sync

# 3. Create Vault auth secret (token method — see docs/vault-auth.md for others)
kubectl create secret generic vault-auth-secret \
  --namespace vault-etcd-sync \
  --from-literal=authMethod=token \
  --from-literal=vaultAddress=https://vault.example.com \
  --from-literal=token=s.xxxxxxxx

# 4. Create etcd credentials secret
kubectl create secret generic etcd-auth-secret \
  --namespace vault-etcd-sync \
  --from-literal=username=etcduser \
  --from-literal=password=etcdpassword

# 5. Apply a VaultEtcdSync CR
kubectl apply -f docs/examples/basic.yaml

# 6. Deploy via Helm
helm upgrade --install hermes helm/vault-etcd-sync-operator \
  --namespace vault-etcd-sync \
  --create-namespace \
  --set image.repository=728921286627.dkr.ecr.us-east-1.amazonaws.com/dev/hermes \
  --set image.tag=latest
```

Check sync status:

```bash
kubectl get vaultedcsyncs -n vault-etcd-sync
kubectl describe vaultedcsync gravity-staging-config -n vault-etcd-sync
```

---

## Installation

### Prerequisites

| Requirement | Version |
|---|---|
| Kubernetes | 1.28+ |
| HashiCorp Vault | 1.12+ (KV v2 engine) |
| etcd | 3.5+ |
| Helm | 3.x |

### Helm install

```bash
helm upgrade --install hermes helm/vault-etcd-sync-operator \
  --namespace vault-etcd-sync \
  --create-namespace \
  --values my-values.yaml
```

Minimal `my-values.yaml`:

```yaml
image:
  repository: 728921286627.dkr.ecr.us-east-1.amazonaws.com/dev/hermes
  tag: "1.0.0"

replicaCount: 2
leaderElection:
  enabled: true     # required when replicaCount > 1

metrics:
  enabled: true
  serviceMonitor:
    enabled: true
    labels:
      prometheus: kube-prometheus
```

### CRD only

```bash
kubectl apply -f config/crd/bases/sync.example.com_vaultedcsyncs.yaml
```

---

## Building

```bash
make build                   # compile binary locally
make docker-build IMG=...    # native image on current machine

# Multi-arch manifest workflow (build on each host, combine)
make docker-build-amd64 IMG=registry/hermes:1.0.0   # run on amd64 host
make docker-build-arm64 IMG=registry/hermes:1.0.0   # run on arm64 host
make docker-manifest    IMG=registry/hermes:1.0.0   # combine + push manifest
```

---

## Security

- Secret **values** are never logged — only key names appear in logs
- Vault tokens are scrubbed from error messages before reaching K8s Events
- Pod runs as **non-root UID 65532** on a `distroless/static` base image
- `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`
- Service account JWT mounted as a projected volume with `expirationSeconds: 3600`
- Template rendering fails safe — partial output is **never** written to etcd
