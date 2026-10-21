package toolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type reviewMemory struct {
	held, maximum int64
	nilLease      bool
}

var errReviewLimit = errors.New("aggregate review state ceiling")

func TestNativeValueDomainsCannotAcquireOperationMeaning(t *testing.T) {
	memory := &reviewMemory{maximum: 64 << 20}
	args := map[string]any{"path": "fixture\xff"}
	seen := map[string]bool{}
	for _, domain := range []NativeValueDomain{OperationArgumentsDomain, ApprovalAuthorityDomain, DiagnosticInspectionDomain} {
		value, release, err := NewNativeValue(t.Context(), memory, domain, args)
		if err != nil {
			t.Fatal(err)
		}
		if !value.Valid() || seen[value.Digest()] {
			t.Fatal("native semantic domains aliased")
		}
		seen[value.Digest()] = true
		decoded, releaseDecoded, err := value.Value(t.Context(), memory)
		if err != nil || !reflect.DeepEqual(decoded, args) {
			t.Fatal("neutral codec changed original byte values", err)
		}
		restored, releaseRestored, err := RestoreNativeValue(t.Context(), memory, domain, value.Representation(), value.Digest(), value.Canonical())
		if err != nil || restored != value {
			t.Fatal("neutral persisted value changed", err)
		}
		if domain != OperationArgumentsDomain {
			if _, releaseWrong, err := RestoreReviewedOperationInputs(t.Context(), memory, value.Representation(), value.Digest(), value.Canonical()); err == nil || releaseWrong != nil {
				t.Fatal("authority or inspection became executable arguments")
			}
		} else {
			if value.Canonical() != `["operation_arguments",1,["object",[[["utf8","path"],["bytes_base64","Zml4dHVyZf8="]]]]]` {
				t.Fatal("existing operation representation bytes changed")
			}
		}
		releaseRestored()
		releaseDecoded()
		release()
	}
	if _, release, err := NewNativeValue(t.Context(), memory, NativeValueDomain("unregistered"), args); err == nil || release != nil {
		t.Fatal("unknown native semantic domain accepted")
	}
	if memory.held != 0 {
		t.Fatal("neutral codec leaked admitted state")
	}
}

func (m *reviewMemory) ReserveProgramState(ctx context.Context, n int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.nilLease {
		return nil, nil
	}
	if n > m.maximum-m.held {
		return nil, errReviewLimit
	}
	m.held += n
	return func() { m.held -= n }, nil
}

func TestReviewedInputsPreserveRawScalarAndKeyIdentity(t *testing.T) {
	memory := &reviewMemory{maximum: 64 << 20}
	identities := map[string]bool{}
	for _, value := range []string{"bad\xff", "bad\xfe", "bad\ufffd"} {
		original := map[string]any{value: []any{value, []any{"bytes_base64", "YmFk/w=="}, map[string]any{"utf8": "literal tag"}}, "integer": json.Number("9007199254740993"), "float32": float32(0.1), "bool": true, "null": nil}
		reviewed, release, err := NewReviewedOperationInputs(t.Context(), memory, original)
		if err != nil {
			t.Fatal(err)
		}
		if !reviewed.Valid() || reviewed.Representation() != LosslessOperationArgumentsV1 || identities[reviewed.ArgumentsDigest()] {
			t.Fatal("raw byte identity collided")
		}
		identities[reviewed.ArgumentsDigest()] = true
		if !strings.Contains(reviewed.CanonicalArguments(), `["number","0.1"]`) {
			t.Fatal("float32 scalar width changed")
		}
		decoded, releaseDecoded, err := reviewed.Arguments(t.Context(), memory)
		if err != nil {
			t.Fatal(err)
		}
		values, ok := decoded[value].([]any)
		if !ok || values[0] != value || !reflect.DeepEqual(values[1], []any{"bytes_base64", "YmFk/w=="}) || decoded["integer"] != json.Number("9007199254740993") {
			t.Fatalf("original byte values changed: %#v", decoded)
		}
		restored, releaseRestored, err := RestoreReviewedOperationInputs(t.Context(), memory, reviewed.Representation(), reviewed.ArgumentsDigest(), reviewed.CanonicalArguments())
		if err != nil {
			t.Fatal(err)
		}
		if restored != reviewed {
			t.Fatal("persisted review identity changed")
		}
		releaseRestored()
		releaseRestored()
		releaseDecoded()
		release()
		release()
		if memory.held != 0 {
			t.Fatal("review leases leaked")
		}
	}
}

func TestReviewedInputsKnownGrammarAndCanonicalKeys(t *testing.T) {
	memory := &reviewMemory{maximum: 64 << 20}
	cases := []string{
		`["operation_arguments",2,["object",[]]]`,
		`["operation_arguments",1,["object",[[["utf8","b"],["null"]],[["utf8","a"],["null"]]]]]`,
		`["operation_arguments",1,["object",[[["utf8","a"],["null"]],[["utf8","a"],["null"]]]]]`,
		`["operation_arguments",1,["object",[[["bytes_base64","YQ=="],["null"]]]]]`,
		`["operation_arguments",1,["object",[[["utf8","a"],["number","NaN"]]]]]`,
		`["operation_arguments",1,["object",[[["utf8","a"],["bool","true"]]]]]`,
		`["operation_arguments",1,["object",[[["utf8","a"],["null",0]]]]]`,
		`["operation_arguments",1,["array",[]]]`,
		`["operation_arguments",1,["object",[]]] {}`,
		` ["operation_arguments",1,["object",[]]]`,
	}
	for _, raw := range cases {
		sum := sha256.Sum256([]byte(raw))
		if _, release, err := RestoreReviewedOperationInputs(t.Context(), memory, LosslessOperationArgumentsV1, hex.EncodeToString(sum[:]), raw); err == nil || release != nil {
			t.Errorf("unknown/noncanonical shape accepted: %s", raw)
		}
		if memory.held != 0 {
			t.Fatal("refused restore leaked lease")
		}
	}
	if _, _, err := RestoreReviewedOperationInputs(t.Context(), memory, "unknown", "", "{}"); err == nil {
		t.Fatal("unknown representation accepted")
	}
	if _, _, err := RestoreReviewedOperationInputs(t.Context(), memory, LosslessOperationArgumentsV1, "wrong", "{}"); err == nil {
		t.Fatal("incorrect digest accepted")
	}
}

func TestReviewedInputsAdmitDepthPayloadAndReleaseOnFailure(t *testing.T) {
	memory := &reviewMemory{maximum: 65536}
	cycle := map[string]any{}
	cycle["cycle"] = cycle
	for _, arguments := range []map[string]any{cycle, {"large": strings.Repeat("x", 65536)}, {"invalid": json.Number("bad")}, {"invalid": func() {}}} {
		if _, release, err := NewReviewedOperationInputs(t.Context(), memory, arguments); err == nil || release != nil {
			t.Fatal("unbounded/invalid review admitted")
		}
		if memory.held != 0 {
			t.Fatal("failed review leaked reservations")
		}
	}
	memory.nilLease = true
	if _, _, err := NewReviewedOperationInputs(t.Context(), memory, map[string]any{}); err == nil {
		t.Fatal("missing state lease accepted")
	}
	memory.nilLease = false
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := NewReviewedOperationInputs(ctx, memory, map[string]any{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	// Review depth is charged by the host ledger; query-depth is not a new
	// restriction on command argument objects.
	memory.maximum = 8 << 20
	var nested any = "deep"
	for i := 0; i < 256; i++ {
		nested = []any{nested}
	}
	reviewed, release, err := NewReviewedOperationInputs(t.Context(), memory, map[string]any{"nested": nested})
	if err != nil {
		t.Fatal(err)
	}
	_, releaseDecoded, err := reviewed.Arguments(t.Context(), memory)
	if err != nil {
		t.Fatal(err)
	}
	releaseDecoded()
	release()
	if memory.held != 0 {
		t.Fatal("nested review leaked retained state")
	}
}
