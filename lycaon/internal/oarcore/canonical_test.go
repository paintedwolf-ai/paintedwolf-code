package oarcore

import (
	"math"
	"testing"
)

func TestFact28ToolFingerprintNormalizesHostCollections(t *testing.T) {
	a, err := ToolFingerprint("command", map[string]any{"env": map[string]string{"B": "two", "A": "one"}, "args": []string{"a", "b"}})
	if err != nil {
		t.Fatalf("canonical fingerprint: %v", err)
	}
	b, err := ToolFingerprint("command", map[string]any{"args": []any{"a", "b"}, "env": map[string]any{"A": "one", "B": "two"}})
	if err != nil {
		t.Fatalf("canonical fingerprint: %v", err)
	}
	if a != b {
		t.Fatalf("[OAR-FACT-28] JSON-equivalent host collections differ: %s != %s", a, b)
	}
	c, err := ToolFingerprint("different", map[string]any{"args": []string{"a", "b"}, "env": map[string]string{"A": "one", "B": "two"}})
	if err != nil {
		t.Fatalf("canonical fingerprint: %v", err)
	}
	if c == a {
		t.Fatal("[OAR-FACT-28] tool identity omitted")
	}
}

func TestFact28ToolFingerprintRejectsLossyOrInvalidNumbers(t *testing.T) {
	for _, value := range []any{int64(9007199254740993), int64(math.MaxInt64), math.Inf(1), math.NaN()} {
		if _, err := ToolFingerprint("read", map[string]any{"value": value}); err == nil {
			t.Fatalf("[OAR-FACT-28] accepted %v", value)
		}
	}
}
