package toolexecution

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

type programStateFunc func(context.Context, int64) (func(), error)

func (f programStateFunc) ReserveProgramState(ctx context.Context, n int64) (func(), error) {
	return f(ctx, n)
}

func TestNativeArgumentsFreezeNestedReviewedInputsAndReleaseOnce(t *testing.T) {
	input := map[string]any{"nested": map[string]any{"list": []any{"before"}}, "argv": []string{"original"}}
	admission := &stageAdmissionFixture{}
	snapshot, err := prepareNativeArguments(t.Context(), admission, input)
	testutil.FailErr(t, "freeze canonical inputs", err)
	input["nested"].(map[string]any)["list"].([]any)[0] = "changed"
	input["argv"].([]string)[0] = "changed"
	if snapshot.values["nested"].(map[string]any)["list"].([]any)[0] != "before" || snapshot.values["argv"].([]string)[0] != "original" || admission.held == 0 {
		t.Fatal("held review aliases caller state or has no reservation")
	}
	snapshot.close()
	snapshot.close()
	if admission.held != 0 {
		t.Fatal("retained inputs leaked reservation")
	}
}

func TestNativeArgumentsDenyBeforeCloneIncludingCyclicHostValues(t *testing.T) {
	input := map[string]any{}
	input["cycle"] = input
	cause := errors.New("aggregate retained state exhausted")
	calls, held := 0, 0
	port := programStateFunc(func(context.Context, int64) (func(), error) {
		calls++
		if calls == 5 {
			return nil, cause
		}
		held++
		return func() { held-- }, nil
	})
	if _, err := prepareNativeArguments(t.Context(), port, input); !errors.Is(err, cause) || held != 0 {
		t.Fatalf("cyclic host input bypassed bounded traversal: %v held%d", err, held)
	}
	bad := programStateFunc(func(context.Context, int64) (func(), error) { return nil, nil })
	if _, err := prepareNativeArguments(t.Context(), bad, map[string]any{}); err == nil {
		t.Fatal("missing state lease admitted")
	}
}
