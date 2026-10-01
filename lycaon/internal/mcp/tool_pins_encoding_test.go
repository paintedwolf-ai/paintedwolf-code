package mcp

import (
	"encoding/json"
	"math"
	"testing"
)

func TestToolFingerprintStoredEncoding(t *testing.T) {
	const want = "975387bebb103c23afb6556ef728e2e17b627b116558a90611efdeb6b5942348"
	if got := readFileFingerprint(t, "Reads a file."); got != want {
		t.Fatalf("tool fingerprint = %s, want %s", got, want)
	}
}

func TestToolFingerprintRejectsUnencodableSchema(t *testing.T) {
	for _, value := range []any{math.NaN(), math.Inf(1), json.Number("invalid"), func() {}} {
		definition := def("tool", "description", map[string]any{"default": value}, false)
		if fingerprint, err := toolDefinitionFingerprint(definition); err == nil || fingerprint != "" {
			t.Fatalf("invalid schema value %T produced fingerprint %q with error %v", value, fingerprint, err)
		}
	}
}
