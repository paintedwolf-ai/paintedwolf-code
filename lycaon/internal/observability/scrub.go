package observability

import (
	"encoding/json"
	"sort"
	"strings"
)

// secretFieldNames trigger whole-value redaction by normalized field name.
var secretFieldNames = []string{
	"api_key",
	"apikey",
	"access_key",
	"auth_key",
	"access_token",
	"refresh_token",
	"client_secret",
	"authorization",
	"password",
	"secret",
	"token",
	"credential",
	"private_key",
}

// secretValueMapNames retain map keys while redacting every leaf value.
var secretValueMapNames = map[string]bool{
	"env":     true,
	"headers": true,
}

// IsSecretFieldName reports whether a normalized field name carries a secret.
func IsSecretFieldName(name string) bool {
	normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(name)))
	if normalized == "" {
		return false
	}
	for _, needle := range secretFieldNames {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

// SecretFieldNames returns the sorted field-name needles.
func SecretFieldNames() []string {
	out := append([]string(nil), secretFieldNames...)
	sort.Strings(out)
	return out
}

// ScrubValue redacts fields and embedded patterns in a decoded value tree.
func ScrubValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, inner := range val {
			if IsSecretFieldName(k) {
				out[k] = redacted
				continue
			}
			if secretValueMapNames[strings.ToLower(strings.TrimSpace(k))] {
				out[k] = redactValueLeaves(inner)
				continue
			}
			out[k] = ScrubValue(inner)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, inner := range val {
			out[i] = ScrubValue(inner)
		}
		return out
	case string:
		return RedactString(val)
	default:
		return val
	}
}

// ScrubDeclaredCredentialJSON redacts every leaf while retaining object keys.
func ScrubDeclaredCredentialJSON(raw []byte) []byte {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return []byte(redacted)
	}
	scrubbed, err := json.Marshal(redactEveryLeaf(decoded))
	if err != nil {
		return []byte(redacted)
	}
	return scrubbed
}

// redactEveryLeaf replaces every scalar with the redaction marker.
func redactEveryLeaf(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, inner := range val {
			out[k] = redactEveryLeaf(inner)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, inner := range val {
			out[i] = redactEveryLeaf(inner)
		}
		return out
	case nil:
		return nil
	default:
		return redacted
	}
}

func redactValueLeaves(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, inner := range val {
			out[k] = redactValueLeaves(inner)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, inner := range val {
			out[i] = redactValueLeaves(inner)
		}
		return out
	case string:
		return redacted
	default:
		return val
	}
}

// ScrubJSON preserves document shape and scrubs invalid input as text.
func ScrubJSON(raw []byte) []byte {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return []byte(RedactString(string(raw)))
	}
	scrubbed, err := json.Marshal(ScrubValue(decoded))
	if err != nil {
		return []byte(RedactString(string(raw)))
	}
	return scrubbed
}

// ScrubHeaders retains header names while redacting their secret values.
func ScrubHeaders(headers map[string][]string) map[string][]string {
	out := make(map[string][]string, len(headers))
	for name, values := range headers {
		if IsSecretFieldName(name) {
			out[name] = []string{redacted}
			continue
		}
		scrubbed := make([]string, len(values))
		for i, v := range values {
			scrubbed[i] = RedactString(v)
		}
		out[name] = scrubbed
	}
	return out
}
