# Observability

---

## Prometheus Metrics

Enable metrics in Helm values (default: enabled on port `8080`):

```yaml
metrics:
  enabled: true
  serviceMonitor:
    enabled: true       # requires prometheus-operator CRDs
    interval: "30s"
    labels:
      prometheus: kube-prometheus
```

### Available metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `vault_etcd_sync_last_sync_timestamp_seconds` | Gauge | `namespace`, `name` | Unix timestamp of the last successful sync |
| `vault_etcd_sync_errors_total` | Counter | `namespace`, `name`, `error_type` | Total sync errors |
| `vault_etcd_sync_secret_version` | Gauge | `namespace`, `name`, `vault_path` | Current Vault KV v2 version number per path |

### `error_type` label values

| Value | Trigger |
|---|---|
| `vault_read` | Failed to authenticate with Vault or read a secret path |
| `template_render` | Template execution failed or output is not valid JSON |
| `etcd_write` | Failed to connect to or write to etcd |

### Example PromQL queries

```promql
# CRs that have not synced successfully in the last 5 minutes
time() - vault_etcd_sync_last_sync_timestamp_seconds > 300

# Error rate per CR over the last 10 minutes
rate(vault_etcd_sync_errors_total[10m])

# Current Vault secret versions
vault_etcd_sync_secret_version
```

---

## Kubernetes Events

Hermes emits K8s Events on the `VaultEtcdSync` object for every sync outcome:

```bash
kubectl describe vaultedcsync gravity-staging-config -n vault-etcd-sync
```

```
Events:
  Type     Reason       Age   From                        Message
  ----     ------       ----  ----                        -------
  Normal   SyncSuccess  2m    vault-etcd-sync-operator    Synced 3 vault paths to etcd key /gravity/staging/app-config
  Warning  SyncFailed   5m    vault-etcd-sync-operator    sync error (vault_read): vault read "secret/data/gravity/staging/postgres": ...
```

---

## Status conditions

```bash
kubectl get ves -n vault-etcd-sync -o wide
```

```
NAME                      ETCDKEY                        INTERVAL   STATUS    LASTSYNC
gravity-staging-config    /gravity/staging/app-config    60s        Success   2026-03-27T10:05:00Z
```

Full condition detail:

```bash
kubectl get vaultedcsync gravity-staging-config -n vault-etcd-sync -o jsonpath='{.status.conditions}' | jq .
```

```json
[
  {
    "type": "Ready",
    "status": "True",
    "reason": "SyncSuccess",
    "message": "Synced 3 vault paths to etcd key /gravity/staging/app-config",
    "lastTransitionTime": "2026-03-27T10:05:00Z"
  }
]
```

---

## Structured logs

Logs are structured JSON (via `go-logr` / `zap`). Every log line includes:

```json
{
  "level": "info",
  "ts": "2026-03-27T10:05:00Z",
  "logger": "controllers.VaultEtcdSync",
  "msg": "sync successful",
  "cr": "vault-etcd-sync/gravity-staging-config",
  "etcdKey": "/gravity/staging/app-config"
}
```

Adjust verbosity with the `--log-level` flag (via Helm `values.yaml`):

```yaml
logLevel: debug    # debug | info | warn | error
```

At `debug` level additional lines are emitted showing vault path key names (never values) and watcher poll results.
