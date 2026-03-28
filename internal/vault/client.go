// Package vault provides a thin client over the HashiCorp Vault API that
// supports three authentication methods (token, userpass, kubernetes) and
// continuous token renewal.
package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	vaultapi "github.com/hashicorp/vault/api"
)

const (
	authMethodToken      = "token"
	authMethodUserpass   = "userpass"
	authMethodKubernetes = "kubernetes"

	defaultUserpassMount   = "userpass"
	defaultKubernetesMount = "kubernetes"

	// serviceAccountTokenPath is the path to the Vault-audience JWT.
	// This is a separate projected volume from the default K8s API token so
	// each token carries the correct audience:
	//   /var/run/secrets/kubernetes.io/serviceaccount/token  → audience: k8s API
	//   /var/run/secrets/vault/token                         → audience: vault
	serviceAccountTokenPath = "/var/run/secrets/vault/token"

	// renewInterval is how often the renewal loop wakes up to renew the token.
	// We target renewing well before the TTL expires.
	renewInterval = 15 * time.Minute
)

// authConfig holds parsed Vault authentication parameters from a K8s Secret.
type authConfig struct {
	AuthMethod   string
	VaultAddress string
	// token auth
	Token string
	// userpass auth
	Username  string
	Password  string
	MountPath string
	// kubernetes auth
	VaultRole string
}

// Client wraps the Vault API client with authentication and token renewal.
//
// Safe for concurrent use; the embedded mutex protects token rotation.
type Client struct {
	vc         *vaultapi.Client
	auth       authConfig
	log        logr.Logger
	mu         sync.RWMutex // guards vc.Token() during renewal
}

// NewClient authenticates against Vault using credentials from secretData and
// starts the background token-renewal loop.
//
// secretData is the raw Data map from a corev1.Secret (byte slices).
// The caller is responsible for calling ctx cancellation to stop the renewal loop.
func NewClient(ctx context.Context, secretData map[string][]byte, log logr.Logger) (*Client, error) {
	auth, err := parseAuthConfig(secretData)
	if err != nil {
		return nil, fmt.Errorf("vault: parse auth config: %w", err)
	}

	cfg := vaultapi.DefaultConfig()
	cfg.Address = auth.VaultAddress

	// Allow optional env-based TLS overrides (VAULT_CACERT, VAULT_SKIP_VERIFY, …).
	if envErr := cfg.ReadEnvironment(); envErr != nil {
		log.V(1).Info("vault: ignoring environment config error", "err", envErr)
	}

	vc, err := vaultapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("vault: create api client: %w", err)
	}

	c := &Client{
		vc:  vc,
		auth: auth,
		log: log.WithName("vault-client").WithValues("authMethod", auth.AuthMethod, "address", auth.VaultAddress),
	}

	if err := c.login(ctx); err != nil {
		return nil, err
	}

	// Start the renewal loop; it stops when ctx is cancelled.
	go c.renewLoop(ctx)

	return c, nil
}

// parseAuthConfig converts raw Secret data bytes into an authConfig.
func parseAuthConfig(data map[string][]byte) (authConfig, error) {
	get := func(k string) string { return strings.TrimSpace(string(data[k])) }

	cfg := authConfig{
		AuthMethod:   get("authMethod"),
		VaultAddress: get("vaultAddress"),
	}

	if cfg.VaultAddress == "" {
		return cfg, fmt.Errorf("vaultAddress is required in vault auth secret")
	}

	switch cfg.AuthMethod {
	case authMethodToken:
		if cfg.Token = get("token"); cfg.Token == "" {
			return cfg, fmt.Errorf("token field is required for token auth method")
		}

	case authMethodUserpass:
		cfg.Username = get("username")
		cfg.Password = get("password")
		if cfg.MountPath = get("mountPath"); cfg.MountPath == "" {
			cfg.MountPath = defaultUserpassMount
		}
		if cfg.Username == "" || cfg.Password == "" {
			return cfg, fmt.Errorf("username and password are required for userpass auth method")
		}

	case authMethodKubernetes:
		if cfg.VaultRole = get("vaultRole"); cfg.VaultRole == "" {
			return cfg, fmt.Errorf("vaultRole is required for kubernetes auth method")
		}
		if cfg.MountPath = get("mountPath"); cfg.MountPath == "" {
			cfg.MountPath = defaultKubernetesMount
		}

	default:
		return cfg, fmt.Errorf("unsupported authMethod %q (valid: token|userpass|kubernetes)", cfg.AuthMethod)
	}

	return cfg, nil
}

// login performs the initial (or re-) authentication and sets the client token.
func (c *Client) login(ctx context.Context) error {
	switch c.auth.AuthMethod {
	case authMethodToken:
		c.mu.Lock()
		c.vc.SetToken(c.auth.Token)
		c.mu.Unlock()
		return nil

	case authMethodUserpass:
		path := fmt.Sprintf("auth/%s/login/%s", c.auth.MountPath, c.auth.Username)
		secret, err := c.vc.Logical().WriteWithContext(ctx, path, map[string]interface{}{
			"password": c.auth.Password,
		})
		if err != nil {
			// Do NOT include password in the error — scrub it.
			return fmt.Errorf("vault userpass login at %q: request failed", path)
		}
		if secret == nil || secret.Auth == nil {
			return fmt.Errorf("vault userpass login at %q: no auth token returned", path)
		}
		c.mu.Lock()
		c.vc.SetToken(secret.Auth.ClientToken)
		c.mu.Unlock()
		return nil

	case authMethodKubernetes:
		jwtBytes, err := os.ReadFile(serviceAccountTokenPath)
		if err != nil {
			return fmt.Errorf("vault kubernetes auth: reading service account JWT: %w", err)
		}
		path := fmt.Sprintf("auth/%s/login", c.auth.MountPath)
		secret, err := c.vc.Logical().WriteWithContext(ctx, path, map[string]interface{}{
			"role": c.auth.VaultRole,
			"jwt":  strings.TrimSpace(string(jwtBytes)),
		})
		if err != nil {
			return fmt.Errorf("vault kubernetes login at %q: %w", path, err)
		}
		if secret == nil || secret.Auth == nil {
			return fmt.Errorf("vault kubernetes login at %q: no auth token returned", path)
		}
		c.mu.Lock()
		c.vc.SetToken(secret.Auth.ClientToken)
		c.mu.Unlock()
		return nil
	}

	return fmt.Errorf("unknown auth method: %s", c.auth.AuthMethod)
}

// renewLoop periodically renews the Vault token. On renewal failure it
// re-authenticates from scratch. The loop exits when ctx is cancelled.
func (c *Client) renewLoop(ctx context.Context) {
	log := c.log.WithValues("method", "renewLoop")
	ticker := time.NewTicker(renewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.V(1).Info("stopping vault token renewal loop")
			return
		case <-ticker.C:
			// RenewSelf makes an outbound HTTP call; do not hold the mutex across it.
			// The vault client itself is goroutine-safe.
			_, err := c.vc.Auth().Token().RenewSelf(0) // 0 → server-chosen increment

			if err == nil {
				log.V(1).Info("vault token renewed")
				continue
			}

			// Renewal failed — attempt a full re-login.
			log.Error(err, "vault token renewal failed; attempting re-authentication")
			if loginErr := c.login(ctx); loginErr != nil {
				log.Error(loginErr, "vault re-authentication failed; will retry next interval")
			} else {
				log.Info("vault re-authentication succeeded")
			}
		}
	}
}

// ─── Secret access ────────────────────────────────────────────────────────────

// GetSecretKV reads a KV v2 secret and returns the unwrapped data map.
//
// path must be the full KV v2 data path, e.g. "secret/data/myapp/db".
// The caller should never log the returned values; log only the keys.
func (c *Client) GetSecretKV(ctx context.Context, path string) (map[string]interface{}, error) {
	// The vault SDK's client is goroutine-safe; no extra lock needed here.
	secret, err := c.vc.Logical().ReadWithContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("vault: read %q: %w", path, err)
	}
	if secret == nil {
		return nil, fmt.Errorf("vault: path %q not found (nil response)", path)
	}

	// KV v2 wraps actual data under secret.Data["data"].
	dataRaw, ok := secret.Data["data"]
	if !ok {
		return nil, fmt.Errorf("vault: path %q missing 'data' field (is this a KV v2 path?)", path)
	}
	dataMap, ok := dataRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("vault: path %q 'data' field is not a map", path)
	}

	return dataMap, nil
}

// SecretVersion holds the KV v2 metadata version information for a path.
type SecretVersion struct {
	Version     int
	UpdatedTime time.Time
}

// GetSecretVersion returns the current version number and update time for a
// KV v2 secret path using the metadata endpoint. This is used for change
// detection without reading the actual secret data.
//
// metadataPath is derived automatically from the data path by replacing
// "/data/" with "/metadata/".
func (c *Client) GetSecretVersion(ctx context.Context, dataPath string) (SecretVersion, error) {
	metaPath := kvDataPathToMetadata(dataPath)
	secret, err := c.vc.Logical().ReadWithContext(ctx, metaPath)
	if err != nil {
		return SecretVersion{}, fmt.Errorf("vault: metadata read %q: %w", metaPath, err)
	}
	if secret == nil {
		return SecretVersion{}, fmt.Errorf("vault: metadata path %q not found", metaPath)
	}

	// current_version is returned as a JSON number (float64 after decode).
	currentVersionRaw, ok := secret.Data["current_version"]
	if !ok {
		return SecretVersion{}, fmt.Errorf("vault: metadata %q missing current_version", metaPath)
	}
	version, err := toInt(currentVersionRaw)
	if err != nil {
		return SecretVersion{}, fmt.Errorf("vault: metadata %q current_version: %w", metaPath, err)
	}

	// Extract the updated_time / created_time for the current version from the
	// nested versions map: {"1": {"created_time": "...", "deletion_time": "", ...}}
	var updatedTime time.Time
	if versionsRaw, ok := secret.Data["versions"]; ok {
		if versions, ok := versionsRaw.(map[string]interface{}); ok {
			vKey := fmt.Sprintf("%d", version)
			if vData, ok := versions[vKey].(map[string]interface{}); ok {
				if ts, ok := vData["created_time"].(string); ok && ts != "" {
					updatedTime, _ = time.Parse(time.RFC3339Nano, ts)
				}
			}
		}
	}

	return SecretVersion{Version: version, UpdatedTime: updatedTime}, nil
}

// kvDataPathToMetadata converts "secret/data/foo/bar" → "secret/metadata/foo/bar".
func kvDataPathToMetadata(path string) string {
	return strings.Replace(path, "/data/", "/metadata/", 1)
}

// toInt converts Vault's JSON-decoded number types to int.
// The Vault API client decodes with UseNumber(), so numbers arrive as
// json.Number. Standard encoding/json without UseNumber gives float64.
func toInt(v interface{}) (int, error) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, fmt.Errorf("parsing json.Number %q: %w", n, err)
		}
		return int(i), nil
	case float64:
		return int(n), nil
	case int:
		return n, nil
	case int64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}
