// Package template renders Go text/template strings against a merged Vault
// secret data map and validates the output as JSON before writing to etcd.
package template

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	syncv1alpha1 "github.com/example/vault-etcd-sync-operator/api/v1alpha1"
)

// MergeVaultData merges secret data maps from multiple Vault paths into a
// single flat map[string]interface{} following the spec rules:
//
//   - Keys from later paths overwrite keys from earlier paths (last-path-wins).
//   - When a VaultPath.Prefix is set, every key from that path is prepended
//     with the prefix before being merged. E.g. prefix="PG_" turns "PASSWORD"
//     into "PG_PASSWORD".
//
// The returned map is safe to pass directly to Render as the template data.
func MergeVaultData(pathData []PathData) map[string]interface{} {
	merged := make(map[string]interface{})
	for _, pd := range pathData {
		for k, v := range pd.Data {
			key := pd.Prefix + k
			merged[key] = v
		}
	}
	return merged
}

// PathData pairs a VaultPath spec entry with the secret data read from Vault.
type PathData struct {
	VaultPath syncv1alpha1.VaultPath
	// Prefix is copied from VaultPath.Prefix for convenience.
	Prefix string
	// Data is the raw KV v2 secret data map.
	Data map[string]interface{}
}

// Render executes jsonTmpl as a Go text/template against data and validates
// that the result is well-formed JSON.
//
// Returns the rendered JSON string. If template execution or JSON validation
// fails, an error is returned and no output string is produced — callers must
// NOT write a partial result to etcd.
//
// Security: template execution uses text/template (not html/template) because
// the output is JSON consumed by internal services, not rendered as HTML.
// The template author is assumed to be trusted (it lives in the CR spec).
func Render(jsonTmpl string, data map[string]interface{}) (string, error) {
	// Parse the template. We add a custom funcmap with a few helpers that are
	// useful for JSON templating.
	tmpl, err := template.New("sync").Funcs(funcMap()).Parse(jsonTmpl)
	if err != nil {
		return "", fmt.Errorf("template parse error: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		// Mask any secret values that leaked into the error message via
		// {{ .SomeKey }} — replace known secret values with "[REDACTED]".
		// We do a best-effort redaction here; log only the sanitised error.
		return "", fmt.Errorf("template execution error: %w", sanitiseTemplateError(err, data))
	}

	rendered := buf.String()

	// Validate the rendered output is parseable JSON before returning it.
	if !json.Valid([]byte(rendered)) {
		// Use a compact representation for the error to avoid echoing secrets.
		return "", fmt.Errorf("rendered output is not valid JSON (check template syntax and quoting)")
	}

	// Re-compact to a canonical form so etcd stores deterministic content.
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(rendered)); err != nil {
		// Should not happen after json.Valid succeeded, but be defensive.
		return "", fmt.Errorf("json compact failed: %w", err)
	}

	return compact.String(), nil
}

// funcMap returns helper functions available inside templates.
func funcMap() template.FuncMap {
	return template.FuncMap{
		// toJSON serialises a value to a JSON string, useful for embedding
		// nested objects inside the template.
		"toJSON": func(v interface{}) (string, error) {
			b, err := json.Marshal(v)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
		// default returns fallback if the primary value is the zero value.
		"default": func(fallback, primary interface{}) interface{} {
			if primary == nil || primary == "" {
				return fallback
			}
			return primary
		},
		// quote wraps a string in JSON double-quotes with escaping.
		"quote": func(s string) string {
			b, _ := json.Marshal(s)
			return string(b)
		},
		// trimSpace trims whitespace from a string value.
		"trimSpace": strings.TrimSpace,
	}
}

// sanitiseTemplateError removes any secret values from an error message to
// prevent accidental logging of credentials.
func sanitiseTemplateError(err error, data map[string]interface{}) error {
	msg := err.Error()
	for _, v := range data {
		if sv, ok := v.(string); ok && sv != "" && len(sv) > 4 {
			msg = strings.ReplaceAll(msg, sv, "[REDACTED]")
		}
	}
	return fmt.Errorf("%s", msg) //nolint:err113 // intentional string wrap
}
