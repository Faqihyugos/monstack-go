package config

import (
	"encoding/json"
	"strings"
)

// RedactedPlaceholder is the string used to mask secret values.
const RedactedPlaceholder = "***"

// Clone creates a deep independent copy of the Config struct.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	var cp Config
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil
	}
	return &cp
}

// Redacted returns a deep copy of Config with sensitive credentials replaced by "***".
func (c *Config) Redacted() *Config {
	if c == nil {
		return nil
	}
	cp := c.Clone()

	// Mask Admin Passwords & Secrets
	if cp.Auth.AdminPassword != "" {
		cp.Auth.AdminPassword = RedactedPlaceholder
	}
	if cp.Auth.AdminPasswordHash != "" {
		cp.Auth.AdminPasswordHash = RedactedPlaceholder
	}
	if cp.Auth.JWT.Secret != "" {
		cp.Auth.JWT.Secret = RedactedPlaceholder
	}
	for i := range cp.Auth.APIKeys {
		if cp.Auth.APIKeys[i].Key != "" {
			cp.Auth.APIKeys[i].Key = RedactedPlaceholder
		}
	}

	// Mask Notification Channel Credentials
	for i := range cp.Alerting.Channels {
		cp.Alerting.Channels[i].Config = redactMap(cp.Alerting.Channels[i].Config)
	}

	// Mask Target Probe Authorization Headers
	for i := range cp.Probes.Targets {
		if cp.Probes.Targets[i].HTTP != nil && cp.Probes.Targets[i].HTTP.Headers != nil {
			newHeaders := make(map[string]string, len(cp.Probes.Targets[i].HTTP.Headers))
			for k, v := range cp.Probes.Targets[i].HTTP.Headers {
				if isSensitiveKey(k) {
					newHeaders[k] = RedactedPlaceholder
				} else {
					newHeaders[k] = v
				}
			}
			cp.Probes.Targets[i].HTTP.Headers = newHeaders
		}
	}

	return cp
}

// isSensitiveKey checks whether a field name implies a secret credential.
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	terms := []string{
		"password", "passwd", "secret", "token", "bot_token",
		"api_key", "apikey", "service_key", "authorization",
		"private_key", "webhook_url",
	}
	for _, term := range terms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

// redactMap traverses arbitrary maps and masks sensitive fields.
func redactMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if isSensitiveKey(k) {
			out[k] = RedactedPlaceholder
			continue
		}
		switch val := v.(type) {
		case map[string]any:
			out[k] = redactMap(val)
		case []any:
			out[k] = redactSlice(val)
		default:
			out[k] = v
		}
	}
	return out
}

// redactSlice traverses slices within generic configs.
func redactSlice(s []any) []any {
	out := make([]any, len(s))
	for i, item := range s {
		switch val := item.(type) {
		case map[string]any:
			out[i] = redactMap(val)
		case []any:
			out[i] = redactSlice(val)
		default:
			out[i] = item
		}
	}
	return out
}
