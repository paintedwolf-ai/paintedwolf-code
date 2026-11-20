package browser

import (
	"encoding/json"
	"testing"
	"time"
)

// Each step's driver call runs under the deadline the host gives its kind,
// and one driver call's budget outlasts the longest of them.
func TestActionBatchGivesEachStepItsDeadline(t *testing.T) {
	actions := []CaptureAction{
		{Type: "click", ActionLocator: ActionLocator{Selector: "#save"}},
		{Type: "wait_for", ActionLocator: ActionLocator{Selector: "#toast"}, Gone: true},
		{Type: "press", Key: "Enter"},
		{Type: "wait"},
	}
	var got []int
	_, err := runActionBatch(actions, func(act CaptureAction) (json.RawMessage, error) {
		got = append(got, act.TimeoutMS)
		return json.RawMessage(`{"ok":true}`), nil
	}, nil)
	if err != nil {
		t.Fatalf("run batch: %v", err)
	}
	want := []int{targetTimeoutMS, waitForTimeoutMS, 0, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step deadlines %v, want %v", got, want)
		}
	}
	if ActionTimeout <= waitForTimeoutMS*time.Millisecond {
		t.Fatalf("driver call budget %v does not outlast the %d ms wait_for deadline", ActionTimeout, waitForTimeoutMS)
	}
}

// A failed step reports the deadline its driver call ran under, how long it
// ran and that the deadline ran out, as the driver measured them.
func TestActionFailureReportsTheDriversDeadline(t *testing.T) {
	res := json.RawMessage(`{"ok":false,"error":"timed out after 30000ms waiting for selector #ready","deadline_ms":30000,"elapsed_ms":30012,"deadline_expired":true}`)
	data := actionFailedData(1, CaptureAction{Type: "wait_for", ActionLocator: ActionLocator{Selector: "#ready"}}, res, nil)
	for key, want := range map[string]any{"step_timeout_ms": float64(30000), "elapsed_ms": float64(30012), "step_deadline_expired": true} {
		if data[key] != want {
			t.Fatalf("%s = %#v, want %#v; data %v", key, data[key], want, data)
		}
	}
	early := actionFailedData(0, CaptureAction{Type: "select"}, json.RawMessage(`{"ok":false,"error":"no matching option"}`), nil)
	if _, reported := early["step_deadline_expired"]; reported {
		t.Fatalf("a failure before the deadline reported one: %v", early)
	}
}
