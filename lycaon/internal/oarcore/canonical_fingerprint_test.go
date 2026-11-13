package oarcore

// ToolFingerprint rejects values that have no canonical JSON encoding
// ([OAR-FACT-20]).

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFact28ToolFingerprintRejectsUnencodable(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"invalid utf-8", "\xff\xfe", "invalid Unicode string"},
		{"invalid number", json.Number("abc"), "invalid number"},
		{"unsupported type", make(chan int), "unsupported JSON value"},
	}
	for _, tc := range cases {
		_, err := ToolFingerprint("read", map[string]any{"v": tc.value})
		if err == nil {
			t.Fatalf("%s: ToolFingerprint accepted", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %q, want substring %q", tc.name, err.Error(), tc.want)
		}
	}
}
