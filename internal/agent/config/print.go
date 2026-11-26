package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// RedactedToken is printed in place of any token value.
const RedactedToken = "asta_…redacted"

// Redacted returns a copy of the configuration with the token replaced.
func (c *Config) Redacted() *Config {
	cp := *c
	if cp.Hub.Token != "" {
		cp.Hub.Token = RedactedToken
	}
	return &cp
}

// YAML renders the effective configuration as YAML with the token
// redacted.
func (c *Config) YAML() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c.Redacted()); err != nil {
		return nil, fmt.Errorf("encode agent config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode agent config: %w", err)
	}
	return buf.Bytes(), nil
}

// Hash is the config_hash reported in Hello: "sha256:" plus the first 16
// hex digits of the SHA-256 of the effective configuration, token excluded.
func (c *Config) Hash() string {
	cp := *c
	cp.Hub.Token = ""
	cp.Hub.TokenFile = ""
	b, err := yaml.Marshal(&cp)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// PrintConfig renders the `print-config` output: redacted YAML plus the
// config hash and token source as trailing comments.
func (c *Config) PrintConfig() ([]byte, error) {
	b, err := c.YAML()
	if err != nil {
		return nil, err
	}
	src := c.TokenSource
	if src == "" {
		switch {
		case c.Hub.Token != "":
			src = "hub.token"
		case c.Hub.TokenFile != "":
			src = "hub.token_file"
		default:
			src = "none"
		}
	}
	b = fmt.Appendf(b, "# config_hash: %s\n# token_source: %s\n", c.Hash(), src)
	return b, nil
}
