# CRD Reference — VaultEtcdSync

`apiVersion: sync.example.com/v1alpha1` · `kind: VaultEtcdSync`

---

## Spec fields

### `spec.vaultPaths`

**Required.** List of Vault KV v2 paths to read. All secret data maps are merged into a single flat map before template rendering.

**Conflict resolution:** if two paths export the same key, the **last path wins**. Use `prefix` to avoid collisions.

```yaml
spec:
  vaultPaths:
    - path: "secret/data/myapp/postgres"
      prefix: ""          # no prefix — keys used as-is
    - path: "secret/data/myapp/mongo"
      prefix: "MONGO_"    # all keys prefixed: USERNAME → MONGO_USERNAME
```

| Field | Type | Required | Description |
|---|---|---|---|
| `path` | string | yes | Full Vault KV v2 path including `/data/`, e.g. `secret/data/myapp/db` |
| `prefix` | string | no | String prepended to every key from this path before merging |

---

### `spec.etcdKey`

**Required.** The key path written in etcd. The rendered JSON is stored as the value.

```yaml
spec:
  etcdKey: "/gravity/staging/app-config"
```

---

### `spec.jsonTemplate`

**Required.** A Go `text/template` string. It is rendered against the merged Vault secret map and the result must be valid JSON. See [Templating Guide](templating.md).

```yaml
spec:
  jsonTemplate: |
    {
      "db": {
        "host": "postgres.svc",
        "user": "{{ .DB_USERNAME }}",
        "pass": "{{ .DB_PASSWORD }}"
      }
    }
```

---

### `spec.syncInterval`

**Default: `60s`.** How often Hermes performs a full re-sync regardless of Vault version changes. Accepts any Go duration string: `30s`, `5m`, `1h`.

```yaml
spec:
  syncInterval: "2m"
```

---

### `spec.vaultRef`

**Required.** Reference to the K8s Secret holding Vault authentication config.

```yaml
spec:
  vaultRef:
    secretRef: "vault-etcd-sync/vault-auth-secret"   # namespace/name
```

See [Vault Authentication](vault-auth.md) for Secret format.

---

### `spec.etcdRef`

**Required.** etcd connection information.

```yaml
spec:
  etcdRef:
    secretRef: "vault-etcd-sync/etcd-auth-secret"    # namespace/name, fields: username, password
    endpoints:
      - "https://etcd-0.etcd:2379"
      - "https://etcd-1.etcd:2379"
      - "https://etcd-2.etcd:2379"
    tlsSecretRef: "vault-etcd-sync/etcd-tls-secret"  # optional, fields: ca.crt, tls.crt, tls.key
```

| Field | Type | Required | Description |
|---|---|---|---|
| `secretRef` | string | yes | `namespace/name` of Secret with `username` and `password` |
| `endpoints` | []string | yes | etcd client endpoints (min 1) |
| `tlsSecretRef` | string | no | `namespace/name` of TLS Secret for mutual TLS |

---

## Status fields

| Field | Description |
|---|---|
| `status.lastSyncTime` | RFC3339 timestamp of the most recent sync attempt |
| `status.lastSyncStatus` | `Success` or `Failed` |
| `status.conditions` | Standard K8s conditions — type `Ready` |

### Condition reasons

| Reason | Meaning |
|---|---|
| `SyncSuccess` | All paths read, template rendered, etcd written successfully |
| `SyncFailed` | Template render error or other non-connectivity failure |
| `VaultUnreachable` | Cannot authenticate with or read from Vault |
| `EtcdUnreachable` | Cannot connect to or write to etcd |

### Checking status

```bash
# Quick overview
kubectl get ves -n vault-etcd-sync

# Full status with conditions
kubectl describe vaultedcsync <name> -n vault-etcd-sync

# Watch sync status in real time
kubectl get ves -n vault-etcd-sync -w
```
