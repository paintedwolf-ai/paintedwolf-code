package jsonvalue

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCanonicalJSONOrdersFieldsWithoutRoundingIntegers(t *testing.T) {
	payload := struct {
		Z int64          `json:"z"`
		A map[string]any `json:"a"`
	}{9007199254740993, map[string]any{"z": "<tag>", "a": []any{true, nil, 1.5}}}
	got, err := CanonicalJSON(payload)
	testutil.FailErr(t, "encode canonical payload", err)
	const want = `{"a":{"a":[true,null,1.5],"z":"\u003ctag\u003e"},"z":9007199254740993}`
	if string(got) != want {
		t.Fatalf("canonical payload = %s, want %s", got, want)
	}
}

func TestCanonicalJSONRejectsInvalidValues(t *testing.T) {
	for _, value := range []any{math.NaN(), math.Inf(1), math.Inf(-1), json.Number("invalid"), func() {}} {
		if got, err := CanonicalJSON(map[string]any{"value": value}); err == nil || got != nil {
			t.Fatalf("invalid value %T produced bytes %q with error %v", value, got, err)
		}
	}
}

func TestCanonicalDigestPreservesExactArgumentIdentity(t *testing.T) {
	first, err := CanonicalDigest(map[string]any{"b": 9007199254740993, "a": "value"})
	testutil.FailErr(t, "hash canonical value", err)
	same, err := CanonicalDigest(json.RawMessage(`{"a":"value","b":9007199254740993}`))
	testutil.FailErr(t, "hash reordered representation", err)
	changed, err := CanonicalDigest(map[string]any{"b": 9007199254740992, "a": "value"})
	testutil.FailErr(t, "hash changed value", err)
	if first != same || first == changed || len(first) != 64 {
		t.Fatal("canonical digest lost exact value identity")
	}
	if _, err := CanonicalDigest(math.NaN()); err == nil {
		t.Fatal("invalid canonical digest accepted")
	}
}
