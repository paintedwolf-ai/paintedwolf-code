package captureprojection

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

type valueStateFixture struct {
	outstanding, peak, maximum, largest int64
	calls                               int
	refuseAt                            int
}

func (s *valueStateFixture) ReserveProgramState(_ context.Context, n int64) (func(), error) {
	s.calls++
	s.largest = max(s.largest, n)
	if s.calls == s.refuseAt || n > s.maximum-s.outstanding {
		return nil, errors.New("fixture retained state refused")
	}
	s.outstanding += n
	s.peak = max(s.peak, s.outstanding)
	return func() { s.outstanding -= n }, nil
}
func valueOptionsFixture() (ValueOptions, *valueStateFixture, *streamMemoryFixture) {
	state := &valueStateFixture{maximum: 64 << 20}
	memory := &streamMemoryFixture{}
	return ValueOptions{State: state, Stream: streamOptionsFixture(memory), MaximumNodes: 1024, MaximumDepth: 64}, state, memory
}

func TestValueScreensRawScalarBytesBeforeAnyJSONEncoding(t *testing.T) {
	projector := testProjector(t, nil)
	key := "raw\xffkey"
	original := map[string]any{key: []any{"literal\xff/" + testSecret, json.Number("9007199254740993")}}
	options, state, memory := valueOptionsFixture()
	projected, metadata, release, err := projector.Value(t.Context(), Scope{ProjectID: "fixture-project", SessionID: "fixture-session"}, "operation.arguments", original, options)
	testutil.FailErr(t, "screen raw native argument values", err)
	defer release()
	if state.outstanding == 0 || memory.outstanding != 0 {
		t.Fatal("projection lifetime was not retained independently of detector scratch")
	}
	values := projected.(map[string]any)[key].([]any)
	text := values[0].(string)
	if !strings.Contains(text, "\xff") || strings.Contains(text, testSecret) || values[1].(json.Number) != "9007199254740993" || metadata.RedactedCount == 0 || !metadata.StructuredCoverage {
		t.Fatalf("raw bytes or exact number changed before screening: %#v %+v", projected, metadata)
	}
	if original[key].([]any)[0].(string) != "literal\xff/"+testSecret {
		t.Fatal("screening mutated actual reviewed inputs")
	}
	if _, _, _, err = projector.Value(t.Context(), Scope{}, "operation.arguments", struct{ Secret string }{testSecret}, options); err == nil {
		t.Fatal("opaque object bypassed string screening")
	}
	var absent *Projector
	if _, _, _, err = absent.Value(t.Context(), Scope{}, "operation.arguments", original, options); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing capture owner admitted values")
	}
}

func TestValueRefusesBeforeAllocatingAndReleasesPartialTraversal(t *testing.T) {
	for _, kind := range []string{"nodes", "depth", "input", "output", "state", "number", "type", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			options, state, memory := valueOptionsFixture()
			value := any(map[string]any{"one": []any{"first", "second"}})
			ctx := t.Context()
			switch kind {
			case "nodes":
				options.MaximumNodes = 2
			case "depth":
				options.MaximumDepth = 1
			case "input":
				options.Stream.Bounds.InputBytes = 4
			case "output":
				options.Stream.Bounds.OutputBytes = 4
			case "state":
				state.refuseAt = 6
			case "number":
				value = json.Number("not-a-number")
			case "type":
				value = struct{ Value string }{testSecret}
			case "cancel":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			projected, _, release, err := testProjector(t, nil).Value(ctx, Scope{}, "capture.arguments", value, options)
			if err == nil || projected != nil || release != nil || state.outstanding != 0 || memory.outstanding != 0 {
				t.Fatalf("partial projection escaped after %s: value=%#v release=%v err=%v state=%d scratch=%d", kind, projected, release != nil, err, state.outstanding, memory.outstanding)
			}
		})
	}
}

func TestValueAggregateScalarLimitsAndLease(t *testing.T) {
	options, state, memory := valueOptionsFixture()
	options.Stream.Bounds.InputBytes = 6
	options.Stream.Bounds.OutputBytes = 6
	projector := testProjector(t, nil)
	projected, _, release, err := projector.Value(t.Context(), Scope{}, "capture.arguments", []any{"abc", "def"}, options)
	testutil.FailErr(t, "screen exact aggregate byte limit", err)
	if projected.([]any)[1] != "def" || state.outstanding == 0 || memory.outstanding != 0 {
		t.Fatal("wrong projection or premature lease release")
	}
	release()
	release()
	if state.outstanding != 0 {
		t.Fatal("retained projection leaked")
	}
	_, _, _, err = projector.Value(t.Context(), Scope{}, "capture.arguments", []any{"abc", "def", "g"}, options)
	var limit *secretmatch.StreamLimitError
	if !errors.As(err, &limit) || limit.Resource != "capture_value_input_bytes" || state.outstanding != 0 {
		t.Fatalf("aggregate +1: %v state=%d", err, state.outstanding)
	}
}

func TestValueContainerArityRefusesBeforeContainerAdmission(t *testing.T) {
	for _, value := range []any{[]any{"a", "b"}, []string{"a", "b"}, map[string]any{"a": "b"}} {
		options, state, _ := valueOptionsFixture()
		options.MaximumNodes = 1
		_, _, _, err := testProjector(t, nil).Value(t.Context(), Scope{}, "capture.arguments", value, options)
		var limit *secretmatch.StreamLimitError
		if !errors.As(err, &limit) || limit.Resource != "capture_value_nodes" || state.largest > 4352 || state.outstanding != 0 {
			t.Fatalf("container exceeded node bound before refusal: %T %v calls=%d held=%d", value, err, state.calls, state.outstanding)
		}
	}
}

func TestValuePreservesNativePrimitiveTypesAndScreensStringArrays(t *testing.T) {
	options, _, _ := valueOptionsFixture()
	original := map[string]any{"signed": int64(-123), "unsigned": uint64(9007199254740993), "float": float64(1.25), "strings": []string{testSecret, "raw\xff"}, "bool": true, "null": nil}
	projected, _, release, err := testProjector(t, nil).Value(t.Context(), Scope{}, "capture.arguments", original, options)
	testutil.FailErr(t, "screen actual native primitive argument types", err)
	defer release()
	result := projected.(map[string]any)
	if result["signed"] != original["signed"] || result["unsigned"] != original["unsigned"] || result["float"] != original["float"] || result["bool"] != true || result["null"] != nil {
		t.Fatalf("primitive identity lost: %#v", result)
	}
	stringsResult := result["strings"].([]string)
	if strings.Contains(stringsResult[0], testSecret) || stringsResult[1] != "raw\xff" {
		t.Fatal("native string-array screening lost exact bytes")
	}
}
