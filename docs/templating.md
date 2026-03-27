# Templating Guide

Hermes uses Go's `text/template` to render the JSON payload written to etcd.

---

## Data model

All secrets from all `spec.vaultPaths` are merged into a single flat `map[string]interface{}`. Template variables are the merged keys.

**Example:**

Vault path `secret/data/myapp/postgres` contains:
```json
{ "USERNAME": "pguser", "PASSWORD": "pgpass" }
```

Vault path `secret/data/myapp/mongo` with `prefix: "MONGO_"` contains:
```json
{ "USERNAME": "mguser", "PASSWORD": "mgpass" }
```

After merging the template data map is:
```
.USERNAME   = "pguser"
.PASSWORD   = "pgpass"
.MONGO_USERNAME = "mguser"
.MONGO_PASSWORD = "mgpass"
```

> **Conflict rule:** if two paths (after prefix is applied) produce the same key, the **last path in the list wins**. Use `prefix` to avoid collisions.

---

## Basic usage

Access a key with `{{ .KEY_NAME }}`:

```yaml
jsonTemplate: |
  {
    "database": {
      "user": "{{ .DB_USERNAME }}",
      "pass": "{{ .DB_PASSWORD }}"
    }
  }
```

---

## Built-in template functions

Hermes adds the following functions on top of the standard Go template builtins.

### `default`

Returns `fallback` when the primary value is nil or an empty string.

```
{{ default "localhost" .DB_HOST }}
```

### `quote`

Wraps a value in JSON double-quotes with proper escaping. Useful when embedding a value that might contain special characters.

```
{{ quote .SOME_VALUE }}
```

### `trimSpace`

Strips leading/trailing whitespace from a string.

```
{{ trimSpace .MESSY_VALUE }}
```

### `toJSON`

Serialises any value to a JSON string. Useful for embedding nested objects or arrays stored in Vault.

```
"metadata": {{ toJSON .METADATA_MAP }}
```

---

## Standard Go template builtins

All standard Go template functions are available: `if`, `range`, `with`, `eq`, `ne`, `and`, `or`, `not`, `printf`, `len`, etc.

```yaml
jsonTemplate: |
  {
    "pool_size": {{ if .POOL_SIZE }}{{ .POOL_SIZE }}{{ else }}10{{ end }},
    "host": "{{ printf "%s.svc.cluster.local" .DB_HOST }}"
  }
```

---

## Validation

Before writing to etcd, Hermes:

1. Executes the template against the merged data map
2. Validates the output with `json.Valid()`
3. Re-compacts the JSON for a deterministic representation

If validation fails, **nothing is written to etcd** and the CR status is set to `SyncFailed`. This prevents partial or malformed configs from reaching consumers.

Common template mistakes that break JSON:

```yaml
# BAD — unquoted string value
"user": {{ .USERNAME }}

# GOOD
"user": "{{ .USERNAME }}"

# BAD — trailing comma
"a": 1,
"b": 2,    ← trailing comma

# GOOD
"a": 1,
"b": 2
```

---

## Full example

```yaml
jsonTemplate: |
  {
    "cloud": {
      "enabled": true,
      "connection": {
        "BUCKET":         "{{ .AZURE_STORAGE_ACCOUNT }}",
        "ACCESS_KEY":     "{{ .AZURE_CLIENT_ID }}",
        "SECRET_KEY":     "{{ .AZURE_CLIENT_SECRET }}",
        "REGION":         "us-east-1",
        "CLOUD_PROVIDER": "adls"
      }
    },
    "sqlalchemy": {
      "enabled": true,
      "connection": {
        "HOST":         "{{ default "postgres.svc" .PG_HOST }}",
        "PORT":         5432,
        "USERNAME":     "{{ .PG_USERNAME }}",
        "PASSWORD":     "{{ .PG_PASSWORD }}",
        "DATABASE":     "mydb",
        "POOL_SIZE":    10,
        "MAX_OVERFLOW": 20
      }
    },
    "pymongo": {
      "enabled": true,
      "connection": {
        "HOST":         "{{ .MONGO_HOST }}",
        "PORT":         27017,
        "USERNAME":     "{{ .MONGO_USERNAME }}",
        "PASSWORD":     "{{ .MONGO_PASSWORD }}",
        "DATABASE":     "mydb"
      }
    }
  }
```
