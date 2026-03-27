# Vault Authentication

Hermes supports three authentication methods, selected via the `authMethod` field in the vault auth K8s Secret.

---

## Method 1: Token

Simplest method. Suitable for development or short-lived automation. For production, prefer `kubernetes` auth so tokens are managed automatically.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: vault-auth-secret
  namespace: vault-etcd-sync
type: Opaque
stringData:
  authMethod: "token"
  vaultAddress: "https://vault.example.com"
  token: "s.xxxxxxxxxxxxxxxx"
```

> **Note:** Token renewal is attempted every 15 minutes. If the token is not renewable (e.g. a root token), renewal is skipped silently.

---

## Method 2: Username / Password (userpass)

Authenticates via Vault's [userpass auth method](https://developer.hashicorp.com/vault/docs/auth/userpass).

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: vault-auth-secret
  namespace: vault-etcd-sync
type: Opaque
stringData:
  authMethod: "userpass"
  vaultAddress: "https://vault.example.com"
  username: "hermes-svc"
  password: "supersecret"
  mountPath: "userpass"     # optional, default: userpass
```

Vault policy required for the user:

```hcl
path "secret/data/+/+/*" {
  capabilities = ["read"]
}
path "secret/metadata/+/+/*" {
  capabilities = ["read", "list"]
}
```

---

## Method 3: Kubernetes Service Account (recommended for in-cluster)

Authenticates using the pod's projected service account JWT. No static credentials needed.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: vault-auth-secret
  namespace: vault-etcd-sync
type: Opaque
stringData:
  authMethod: "kubernetes"
  vaultAddress: "https://vault.example.com"
  vaultRole: "hermes-role"
  mountPath: "kubernetes"   # optional, default: kubernetes
```

### Vault-side setup

```bash
# Enable Kubernetes auth
vault auth enable kubernetes

# Configure with your cluster's API server
vault write auth/kubernetes/config \
  kubernetes_host="https://kubernetes.default.svc" \
  kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt

# Create a policy
vault policy write hermes-policy - <<EOF
path "secret/data/*" {
  capabilities = ["read"]
}
path "secret/metadata/*" {
  capabilities = ["read", "list"]
}
EOF

# Bind the role to the operator's ServiceAccount
vault write auth/kubernetes/role/hermes-role \
  bound_service_account_names=hermes-vault-etcd-sync-operator \
  bound_service_account_namespaces=vault-etcd-sync \
  policies=hermes-policy \
  ttl=1h
```

### Annotating the ServiceAccount for Vault

Set the Vault role on the operator's ServiceAccount via Helm values:

```yaml
serviceAccount:
  annotations:
    vault.hashicorp.com/role: "hermes-role"
```

> **JWT TTL:** The projected volume is configured with `expirationSeconds: 3600` so Kubernetes rotates the JWT every hour, which is shorter than the Vault role TTL. This ensures Vault always receives a fresh token on re-authentication.

---

## etcd Credentials

### Username + password

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: etcd-auth-secret
  namespace: vault-etcd-sync
type: Opaque
stringData:
  username: "etcduser"
  password: "etcdpassword"
```

### Mutual TLS (optional)

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: etcd-tls-secret
  namespace: vault-etcd-sync
type: Opaque
stringData:
  ca.crt: |
    -----BEGIN CERTIFICATE-----
    <CA certificate PEM>
    -----END CERTIFICATE-----
  tls.crt: |
    -----BEGIN CERTIFICATE-----
    <Client certificate PEM>
    -----END CERTIFICATE-----
  tls.key: |
    -----BEGIN EC PRIVATE KEY-----
    <Client key PEM>
    -----END EC PRIVATE KEY-----
```

Reference it in your CR:

```yaml
spec:
  etcdRef:
    secretRef: "vault-etcd-sync/etcd-auth-secret"
    tlsSecretRef: "vault-etcd-sync/etcd-tls-secret"
    endpoints:
      - "https://etcd-0.etcd:2379"
```
