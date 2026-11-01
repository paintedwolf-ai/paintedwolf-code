package toolcontract

import (
	"encoding/json"
	"testing"
)

func TestModelJSONRetainsExactCanonicalBytes(t *testing.T) {
	memory := &reviewMemory{maximum: 8 << 20}
	args := map[string]any{"z": []any{"\b\f\u2028<", json.Number("9007199254740993")}, "a": true, "b": []string{"two"}}
	got, release, err := CaptureModelJSON(t.Context(), memory, args)
	if err != nil {
		t.Fatalf("capture model: %v", err)
	}
	want, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("reference model: %v", err)
	}
	if got != string(want) || memory.held == 0 {
		t.Fatalf("captured model %q want %q", got, want)
	}
	release()
	if memory.held != 0 {
		t.Fatalf("retained model bytes: %d", memory.held)
	}
}
func TestModelJSONRefusesInvalidValuesWithoutRetention(t *testing.T) {
	for _, value := range []any{"bad\xff", map[string]any{"bad\xff": "key"}, func() {}, json.Number("invalid")} {
		memory := &reviewMemory{maximum: 8 << 20}
		_, release, err := CaptureModelJSON(t.Context(), memory, map[string]any{"value": value})
		if err == nil || release != nil || memory.held != 0 {
			t.Fatalf("invalid model: %v retained %d", err, memory.held)
		}
	}
}
