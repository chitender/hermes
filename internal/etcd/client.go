// Package etcd provides a thin wrapper over the etcd v3 client that supports
// username/password authentication and optional mutual TLS.
package etcd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	defaultDialTimeout    = 5 * time.Second
	defaultRequestTimeout = 10 * time.Second
)

// Config holds connection parameters derived from K8s Secrets.
type Config struct {
	Endpoints []string
	Username  string
	Password  string
	TLS       *tls.Config // nil → plaintext
}

// ParseConfig builds an etcd Config from raw Secret data maps.
//
// authData is the Data map from the etcd-auth-secret (fields: username, password).
// tlsData is the Data map from the etcd-tls-secret (fields: ca.crt, tls.crt, tls.key).
// tlsData may be nil when TLS is not configured.
func ParseConfig(endpoints []string, authData map[string][]byte, tlsData map[string][]byte) (Config, error) {
	cfg := Config{
		Endpoints: endpoints,
		Username:  strings.TrimSpace(string(authData["username"])),
		Password:  strings.TrimSpace(string(authData["password"])),
	}

	if len(tlsData) > 0 {
		tlsCfg, err := buildTLSConfig(tlsData)
		if err != nil {
			return cfg, fmt.Errorf("etcd: building TLS config: %w", err)
		}
		cfg.TLS = tlsCfg
	}

	return cfg, nil
}

// buildTLSConfig assembles a *tls.Config from PEM-encoded certificate data.
func buildTLSConfig(data map[string][]byte) (*tls.Config, error) {
	caCert := data["ca.crt"]
	clientCert := data["tls.crt"]
	clientKey := data["tls.key"]

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if len(caCert) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("etcd: failed to parse CA certificate")
		}
		tlsCfg.RootCAs = pool
	}

	if len(clientCert) > 0 && len(clientKey) > 0 {
		cert, err := tls.X509KeyPair(clientCert, clientKey)
		if err != nil {
			return nil, fmt.Errorf("etcd: failed to parse client certificate/key pair: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tlsCfg, nil
}

// Client is a thin wrapper over the etcd v3 client providing convenience
// methods used by the operator.
type Client struct {
	kv  clientv3.KV
	raw *clientv3.Client
	log logr.Logger
}

// NewClient creates and connects an etcd client from cfg.
// The returned Client is ready for use; call Close() when done.
func NewClient(cfg Config, log logr.Logger) (*Client, error) {
	etcdCfg := clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: defaultDialTimeout,
		TLS:         cfg.TLS,
	}

	if cfg.Username != "" {
		etcdCfg.Username = cfg.Username
		etcdCfg.Password = cfg.Password
	}

	raw, err := clientv3.New(etcdCfg)
	if err != nil {
		return nil, fmt.Errorf("etcd: create client: %w", err)
	}

	return &Client{
		kv:  clientv3.NewKV(raw),
		raw: raw,
		log: log.WithName("etcd-client"),
	}, nil
}

// Put writes value under key. It uses a context-bounded timeout so a slow
// etcd cluster cannot block the reconcile loop indefinitely.
func (c *Client) Put(ctx context.Context, key, value string) error {
	reqCtx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()

	_, err := c.kv.Put(reqCtx, key, value)
	if err != nil {
		// Do NOT include value in the error — it may contain secrets.
		return fmt.Errorf("etcd: put key %q: %w", key, err)
	}

	c.log.V(1).Info("etcd: key written", "key", key, "valueBytes", len(value))
	return nil
}

// Get retrieves the value for key. Returns ("", nil) when the key does not exist.
func (c *Client) Get(ctx context.Context, key string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()

	resp, err := c.kv.Get(reqCtx, key)
	if err != nil {
		return "", fmt.Errorf("etcd: get key %q: %w", key, err)
	}
	if len(resp.Kvs) == 0 {
		return "", nil
	}
	return string(resp.Kvs[0].Value), nil
}

// Close releases the underlying etcd client connection.
func (c *Client) Close() error {
	return c.raw.Close()
}
