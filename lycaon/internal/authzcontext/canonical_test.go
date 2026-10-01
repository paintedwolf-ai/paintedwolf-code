package authzcontext

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

func TestVerifyRejectsUnsupportedStoredHashVersions(t *testing.T) {
	contextRow := Context{SessionID: "session", ContextSeq: 1, HashVersion: HashVersion1}
	var err error
	contextRow.RowHash, err = ComputeRowHash(HashVersion1, "session", 1, "", chainPayloadFromContext(contextRow), "")
	testutil.FailErr(t, "hash context", err)
	eventRow := Event{SessionID: "session", EventSeq: 1, HashVersion: HashVersion1}
	eventRow.RowHash, err = ComputeEventRowHash(HashVersion1, "session", 1, "", eventChainPayloadFromEvent(eventRow), "")
	testutil.FailErr(t, "hash event", err)
	if broken := VerifyContexts("session", []Context{contextRow}); broken != nil {
		t.Fatalf("valid context rejected: %+v", broken)
	}
	if broken := VerifyEvents("session", []Event{eventRow}); broken != nil {
		t.Fatalf("valid event rejected: %+v", broken)
	}
	for _, version := range []int{0, -1, 2} {
		contextRow.HashVersion = version
		eventRow.HashVersion = version
		if broken := VerifyContexts("session", []Context{contextRow}); broken == nil || broken.Reason != "unsupported hash_version" {
			t.Fatalf("context version %d: %+v", version, broken)
		}
		if broken := VerifyEvents("session", []Event{eventRow}); broken == nil || broken.Reason != "unsupported hash_version" {
			t.Fatalf("event version %d: %+v", version, broken)
		}
	}
}
