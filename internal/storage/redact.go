package storage

import (
	"encoding/json"
	"regexp"
	"strings"
)

// secretKeyPattern matches configuration keys whose values must never be
// persisted. Erring on the side of redaction is intentional.
var secretKeyPattern = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?key|secret|token|password|passwd|credential|authorization|bearer|private[_-]?key)`)

// RedactJSON removes secret-looking values from adapter configuration before it
// is written to disk. It returns the redacted document and whether anything
// changed.
func RedactJSON(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return raw, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Not valid JSON: replace entirely rather than risk persisting a secret.
		return json.RawMessage(`"<redacted: invalid configuration>"`), true
	}
	redacted, changed := redactValue(v)
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(redacted)
	if err != nil {
		return json.RawMessage(`"<redacted>"`), true
	}
	return out, true
}

func redactValue(v any) (any, bool) {
	switch value := v.(type) {
	case map[string]any:
		changed := false
		out := make(map[string]any, len(value))
		for k, item := range value {
			if isSecretKey(k) {
				out[k] = "<redacted>"
				changed = true
				continue
			}
			redacted, itemChanged := redactValue(item)
			out[k] = redacted
			changed = changed || itemChanged
		}
		return out, changed
	case []any:
		changed := false
		out := make([]any, len(value))
		for i, item := range value {
			redacted, itemChanged := redactValue(item)
			out[i] = redacted
			changed = changed || itemChanged
		}
		return out, changed
	case string:
		if looksLikeSecretValue(value) {
			return "<redacted>", true
		}
		return value, false
	default:
		return v, false
	}
}

// isSecretKey distinguishes credentials from token *counts*. Keys such as
// input_tokens, output_tokens, and total_tokens describe usage; redacting them
// would destroy reproducibility metadata and falsely mark the config redacted.
// Singular "token" (for example auth_token) is treated as a credential.
func isSecretKey(key string) bool {
	lower := strings.ToLower(key)
	if strings.Contains(lower, "tokens") || strings.Contains(lower, "token_count") {
		return false
	}
	return secretKeyPattern.MatchString(key)
}

// looksLikeSecretValue catches obvious bearer tokens and prefixed API keys in
// string values whose key did not look sensitive.
func looksLikeSecretValue(s string) bool {
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "bearer ") {
		return true
	}
	for _, prefix := range []string{"sk-", "ghp_", "gho_", "xoxb-", "pk_"} {
		if strings.HasPrefix(s, prefix) && len(s) >= len(prefix)+8 {
			return true
		}
	}
	return false
}

// RedactEnv redacts environment values by name while preserving the names, so
// diagnostics can record which variables were provided.
func RedactEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for name, value := range env {
		if isSecretKey(name) {
			out[name] = "<redacted>"
			continue
		}
		out[name] = value
	}
	return out
}
