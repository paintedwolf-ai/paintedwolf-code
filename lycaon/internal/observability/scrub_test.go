package observability

import (
	"encoding/json"
	"strings"
	"testing"
)

const fakeKey = "sk-live-0123456789abcdefghijklmnopqrstuv"

func TestIsSecretFieldName(t *testing.T) {
	secret := []string{
		"api_key", "apiKey", "API_KEY", "X-Api-Key", "access_token", "refresh_token",
		"client_secret", "Authorization", "password", "user_password",
		"token", "credential", "private_key",
	}
	for _, name := range secret {
		if !IsSecretFieldName(name) {
			t.Fatalf("IsSecretFieldName(%q) = false, want true", name)
		}
	}

	notSecret := []string{"", "provider_id", "model", "base_url", "status", "version"}
	for _, name := range notSecret {
		if IsSecretFieldName(name) {
			t.Fatalf("IsSecretFieldName(%q) = true, want false", name)
		}
	}
}

func TestScrubValueRedactsWriteOnlyMapValues(t *testing.T) {
	got, ok := ScrubValue(map[string]any{
		"env":     map[string]any{"INNOCUOUS_NAME": "opaque-credential"},
		"headers": map[string]any{"X-Custom": "opaque-header-value"},
		"url":     "https://example.com",
	}).(map[string]any)
	if !ok {
		t.Fatalf("scrubbed value has unexpected type")
	}
	if got["url"] != "https://example.com" {
		t.Fatalf("non-secret context changed: %#v", got)
	}
	for _, field := range []string{"env", "headers"} {
		values, valid := got[field].(map[string]any)
		if !valid {
			t.Fatalf("%s map shape was not preserved: %#v", field, got[field])
		}
		for name, value := range values {
			if value != redacted {
				t.Fatalf("%s[%s] = %#v, want redacted", field, name, value)
			}
		}
	}
}

func TestScrubValueRedactsByKeyAtEveryDepth(t *testing.T) {
	in := map[string]any{
		"provider_id": "anthropic",
		"api_key":     fakeKey,
		"nested": map[string]any{
			"client_secret": fakeKey,
			"model":         "claude",
		},
		"servers": []any{
			map[string]any{"name": "docs", "authorization": "Bearer " + fakeKey},
		},
	}

	out, ok := ScrubValue(in).(map[string]any)
	if !ok {
		t.Fatal("ScrubValue did not return a map")
	}

	if out["provider_id"] != "anthropic" {
		t.Fatalf("non-secret field was altered: %v", out["provider_id"])
	}
	if out["api_key"] != "[REDACTED]" {
		t.Fatalf("api_key = %v, want [REDACTED]", out["api_key"])
	}

	nested := out["nested"].(map[string]any)
	if nested["client_secret"] != "[REDACTED]" {
		t.Fatalf("nested client_secret leaked: %v", nested["client_secret"])
	}
	if nested["model"] != "claude" {
		t.Fatalf("nested non-secret altered: %v", nested["model"])
	}

	server := out["servers"].([]any)[0].(map[string]any)
	if server["authorization"] != "[REDACTED]" {
		t.Fatalf("authorization inside a slice leaked: %v", server["authorization"])
	}
}

func TestScrubJSONKeepsShapeAndDropsSecrets(t *testing.T) {
	raw := []byte(`{"provider_id":"openai","api_key":"` + fakeKey + `","models":["a","b"]}`)

	got := ScrubJSON(raw)
	if strings.Contains(string(got), fakeKey) {
		t.Fatalf("ScrubJSON leaked the key: %s", got)
	}

	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("ScrubJSON produced invalid JSON: %v", err)
	}
	if decoded["provider_id"] != "openai" {
		t.Fatalf("shape not preserved: %v", decoded)
	}
	if len(decoded["models"].([]any)) != 2 {
		t.Fatalf("array not preserved: %v", decoded["models"])
	}
}

// A capture that failed to parse is exactly when a raw key is most likely to be
// sitting in the buffer, so non-JSON input must still be scrubbed, not passed
// through.
func TestScrubJSONScrubsNonJSONAsText(t *testing.T) {
	raw := []byte("GET /v1/models\nAuthorization: Bearer " + fakeKey + "\n")

	got := ScrubJSON(raw)
	if strings.Contains(string(got), fakeKey) {
		t.Fatalf("non-JSON input leaked the key: %s", got)
	}
}

func TestScrubHeadersKeepsNamesDropsValues(t *testing.T) {
	in := map[string][]string{
		"Authorization": {"Bearer " + fakeKey},
		"X-Api-Key":     {fakeKey},
		"Content-Type":  {"application/json"},
	}

	out := ScrubHeaders(in)

	if _, ok := out["Authorization"]; !ok {
		t.Fatal("header name was dropped: a reader must still see that auth was present")
	}
	if out["Authorization"][0] != "[REDACTED]" {
		t.Fatalf("Authorization value leaked: %v", out["Authorization"])
	}
	if out["X-Api-Key"][0] != "[REDACTED]" {
		t.Fatalf("X-Api-Key value leaked: %v", out["X-Api-Key"])
	}
	if out["Content-Type"][0] != "application/json" {
		t.Fatalf("benign header altered: %v", out["Content-Type"])
	}
}

func TestScrubValueStillRedactsEmbeddedPatterns(t *testing.T) {
	// The value is under a benign key, so only pattern matching can catch it.
	in := map[string]any{"message": "failed with Authorization: Bearer " + fakeKey}

	out := ScrubValue(in).(map[string]any)
	if strings.Contains(out["message"].(string), fakeKey) {
		t.Fatalf("embedded bearer token leaked: %v", out["message"])
	}
}

func TestSecretFieldNamesIsSortedAndNonEmpty(t *testing.T) {
	names := SecretFieldNames()
	if len(names) == 0 {
		t.Fatal("SecretFieldNames is empty")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("SecretFieldNames is not sorted: %v", names)
		}
	}
}
