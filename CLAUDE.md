# CLAUDE.md — Vault-Etcd-Sync Operator (Hermes)

## Project Overview

**Hermes** (vault-etcd-sync-operator) is a Kubernetes operator that synchronizes secrets from HashiCorp Vault into etcd as structured JSON. It is built for Innovaccer's multi-tenant infrastructure where each tenant gets its own etcd key with tenant-specific configuration while sharing Vault-sourced credentials.

- **Owner:** Infrastructure Engineering team at Innovaccer
- **Primary maintainer:** Chitender (GitHub: chitender)
- **Contributor:** Vijay Kushwaha (documentation/Helm chart integration)

## Branch Context

This is the **`main`** branch — the stable base with the initial operator implementation and multi-arch Docker support. The active development branch is `release/test-v1.0` which contains all bug fixes, CI migration to GitHub Actions, and production hardening.

**If making changes on `main`, ensure they are foundational.** Most feature/fix work should target `release/test-v1.0`.

## Repository Layout

```
api/v1alpha1/          CRD types (VaultEtcdSync spec, status, deepcopy)
cmd/main.go            Operator entrypoint (controller-runtime manager)
internal/
  controller/          Reconciler logic, watcher management, metrics
  vault/               Vault client (token/userpass/k8s auth), watcher (version polling)
  etcd/                etcd client (auth + optional mTLS)
  template/            Go text/template renderer with custom funcs (toJSON, default, quote, trimSpace)
config/
  crd/bases/           Generated CRD manifest (kubebuilder)
  rbac/                Generated RBAC roles
helm/vault-etcd-sync-operator/   Helm chart (deployment, RBAC, ServiceMonitor, examples)
Makefile               Build, generate, test, docker, helm targets
Dockerfile             Multi-stage Go build
```

## Key Technical Details

### CRD: `VaultEtcdSync` (`sync.example.com/v1alpha1`)

- **Short name:** `ves` (e.g., `kubectl get ves`)
- **Scope:** Namespaced
- **Spec fields:** `vaultPaths[]` (path + prefix), `etcdKey`, `jsonTemplate`, `syncInterval`, `vaultRef.secretRef`, `etcdRef.secretRef`, `etcdRef.endpoints`, `etcdRef.tlsSecretRef`
- **Status fields:** `lastSyncTime`, `lastSyncStatus`, `conditions` (type: Ready)
- **Condition reasons:** `SyncSuccess`, `SyncFailed`, `VaultUnreachable`, `EtcdUnreachable`

### Vault Auth Methods (3 supported)

1. **Token:** fields `authMethod:"token"`, `vaultAddress`, `token`
2. **Userpass:** fields `authMethod:"userpass"`, `vaultAddress`, `username`, `password`, `mountPath`
3. **Kubernetes:** fields `authMethod:"kubernetes"`, `vaultAddress`, `vaultRole`, `mountPath` — reads SA JWT from `/var/run/secrets/vault/token`

### Reconciliation Flow

1. Parse syncInterval -> Read Vault auth secret -> Build Vault client
2. Read all Vault paths, merge with prefix (last-path-wins)
3. Render jsonTemplate against merged data -> Validate JSON
4. Read etcd auth secret (+ optional TLS) -> Write to etcdKey
5. Update status -> Start/restart Vault watcher -> Requeue after syncInterval
6. Vault watcher polls metadata for version changes -> triggers immediate reconcile

### Prometheus Metrics

- `vault_etcd_sync_last_sync_timestamp_seconds` (gauge, per CR)
- `vault_etcd_sync_errors_total` (counter, per CR + error type)
- `vault_etcd_sync_secret_version` (gauge, per path)

### Template Custom Functions

`toJSON`, `default`, `quote`, `trimSpace` — available in `jsonTemplate` field.

## Build & Test

```bash
make build          # Build manager binary
make test           # Run unit tests
make manifests      # Regenerate CRD + RBAC from Go markers
make generate       # Regenerate DeepCopy methods
make docker-build   # Build container image
make docker-push    # Push to GHCR
make helm-lint      # Lint Helm chart
```

- **Image:** `ghcr.io/chitender/hermes:<tag>`
- **Go version:** 1.22+
- **Key deps:** controller-runtime v0.19.3, vault/api v1.14.0, etcd/client/v3 v3.5.17

## Remotes & Branches

| Remote   | URL                                                  | Purpose       |
|----------|------------------------------------------------------|---------------|
| `origin` | `gitlab.innovaccer.com/infrastructure/hermes.git`    | Internal repo |
| `github` | `github.com/chitender/hermes.git`                    | Public mirror |

- **`main`** — Stable base (initial implementation + multi-arch Docker)
- **`release/test-v1.0`** — Release candidate with all bug fixes and CI migration to GitHub Actions

## Deployment Context

- Deployed via ArgoCD in `gravitymt-prod` namespace
- Helm charts for consuming services live in a separate GitLab repo: `infrastructure/cicd/global-products-helm-chart` (branch: `gravity-dev-mt`)
- `vault-etcd-sync.yaml` template in consuming charts creates per-tenant VaultEtcdSync CRs gated by `global.MULTI_DNS_NAME`
- Prerequisites: `vault-auth-secret` and `etcd-auth-secret` ExternalSecrets deployed by `infra_prereq` chart
- Confluence doc: https://innovaccer.atlassian.net/wiki/spaces/IE/pages/5510431625

## Workflow Rules

1. **Always push changes after committing.** Push to the respective remote branch after every commit:
   - `main` -> `git push origin main` AND `git push github main`
   - `release/test-v1.0` -> `git push origin release/test-v1.0` AND `git push github release/test-v1.0`
2. **Learn from past mistakes.** When a fix is applied for a bug or issue encountered during development, add a note to the "Lessons Learned" section below so the same mistake is not repeated.
3. **Run `make vet` before committing** Go code changes.
4. **Run `make manifests generate` after modifying** `api/v1alpha1/` types.
5. **Do not amend published commits** — always create new commits.

## Lessons Learned

- **Reconcile storm:** `Status().Update()` re-enqueues the CR. Fixed in `release/test-v1.0` by comparing old vs new status before updating.
- **Watcher restart loop:** Watcher was being restarted on every reconcile. Fixed in `release/test-v1.0` by comparing watcher config and reusing if unchanged.
- **Helm CRD ownership:** CRD in `crds/` dir is not labeled by Helm. Fixed in `release/test-v1.0` by moving to `templates/` with `helm.sh/resource-policy: keep`.
- **Projected volume issues:** Must split K8s API token and Vault token into separate projected volumes — Vault needs audience `vault`, controller-runtime needs default audience. Must include `ca.crt` and `namespace` in kube-api-access volume.
- **json.Number type:** Vault returns numbers as `json.Number`, not `int`/`float64`. Must handle type assertion in `toInt()` helper.
- **Double-prefix naming:** Using prefix `MONGO_` on Vault keys already named `MONGO_USER` produces `MONGO_MONGO_USER`. Works but confusing — prefer distinct prefixes or no prefix when Vault key names are already namespaced.
