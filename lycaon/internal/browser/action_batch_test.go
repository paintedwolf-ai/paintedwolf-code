package browser

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestActionBatchPreservesCompletedPrefix(t *testing.T) {
	for _, transportFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "driver", true: "transport"}[transportFailure], func(t *testing.T) {
			actions := []CaptureAction{
				{Type: "click", ActionLocator: ActionLocator{Selector: "#first"}},
				{Type: "click", ActionLocator: ActionLocator{Selector: "#second"}},
				{Type: "click", ActionLocator: ActionLocator{Selector: "#disabled"}},
				{Type: "click", ActionLocator: ActionLocator{Selector: "#unreached"}},
			}
			calls := 0
			var observed []string
			results, err := runActionBatch(actions, func(act CaptureAction) (json.RawMessage, error) {
				calls++
				if act.Selector == "#disabled" {
					if transportFailure {
						return nil, errors.New("connection lost after dispatch")
					}
					return json.RawMessage(`{"ok":false,"error":"control not enabled","state":{"interactive":[{"tag":"button","name":"Cell 5","text":"O","disabled":true}]}}`), nil
				}
				return json.RawMessage(`{"ok":true}`), nil
			}, func(act CaptureAction, _ json.RawMessage) error {
				observed = append(observed, act.Selector)
				return nil
			})
			var rej *browserengine.RejectError
			if !errors.As(err, &rej) || rej.Code != "CAPTURE_ACTION_FAILED" {
				t.Fatalf("batch error = %v", err)
			}
			if calls != 3 || len(results) != 2 || rej.Data["index"] != 2 {
				t.Fatalf("calls=%d results=%d rejection=%+v", calls, len(results), rej)
			}
			if !reflect.DeepEqual(observed, []string{"#first", "#second"}) {
				t.Fatalf("observed = %v", observed)
			}
			completed := rej.Data["completed_actions"].([]completedAction)
			if len(completed) != 2 || completed[1].Index != 1 || completed[1].Locators != "selector=#second" {
				t.Fatalf("completed prefix = %+v", completed)
			}
			if !transportFailure {
				controls := rej.Data["interactive_controls"].([]actionControl)
				if len(controls) != 1 || controls[0].Disabled == nil || !*controls[0].Disabled || controls[0].Text != "O" {
					t.Fatalf("failure state = %+v", controls)
				}
			}
		})
	}
}

func TestActionBatchBoundsBeforeEffects(t *testing.T) {
	_, err := runActionBatch(make([]CaptureAction, MaxCaptureActions+1), func(CaptureAction) (json.RawMessage, error) {
		t.Fatal("oversized batch executed an action")
		return nil, nil
	}, nil)
	var rej *browserengine.RejectError
	if !errors.As(err, &rej) || rej.Code != "CAPTURE_ACTIONS_BOUNDS" {
		t.Fatalf("bounds error = %v", err)
	}
}

func TestActionBatchObserverFailureKeepsItsMeaning(t *testing.T) {
	want := browserengine.Reject("CAPTURE_OUTPUT_OVERSIZED", nil)
	results, err := runActionBatch([]CaptureAction{{Type: "click"}}, func(CaptureAction) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}, func(CaptureAction, json.RawMessage) error { return want })
	if !errors.Is(err, want) || len(results) != 1 {
		t.Fatalf("completed action lost on observer failure: results=%s err=%v", results, err)
	}
}

func TestActionFailureControlsAreBoundedAndExplicit(t *testing.T) {
	var raw []any
	for range 15 {
		raw = append(raw, map[string]any{"tag": "button", "name": strings.Repeat("é", 200), "text": "X", "disabled": false, "unexpected": "discard"})
	}
	controls, truncated := actionControls(raw)
	if len(controls) != 12 || !truncated || !utf8.ValidString(controls[0].Name) || len(controls[0].Name) > 160 {
		t.Fatalf("controls are not bounded: %+v, truncated=%v", controls, truncated)
	}
	b, err := json.Marshal(controls)
	testutil.FailErr(t, "marshal bounded controls", err)
	if strings.Contains(string(b), "unexpected") || !strings.Contains(string(b), `"disabled":false`) {
		t.Fatalf("control projection = %s", b)
	}
	controls[0].Name = "Cell 1"
	*controls[0].Disabled = true
	if got := formatInteractive(controls[:1]); !strings.Contains(got, `(text "X")`) || !strings.Contains(got, "(disabled)") {
		t.Fatalf("failure inventory = %q", got)
	}
}
