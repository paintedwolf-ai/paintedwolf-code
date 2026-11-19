package browser

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A step's driver call runs under the deadline the host gives it, and the
// host's own call budget outlasts that deadline or the driver's default.
func TestStepDeadlineIsOutlastedByTheDriverBudget(t *testing.T) {
	for _, tc := range []struct {
		args map[string]any
		step time.Duration
	}{
		{args: withTimeout(map[string]any{}, targetTimeoutMS), step: targetTimeoutMS * time.Millisecond},
		{args: withTimeout(map[string]any{}, waitForTimeoutMS), step: waitForTimeoutMS * time.Millisecond},
		{args: map[string]any{}, step: waitForTimeoutMS * time.Millisecond},
	} {
		if budget := driverBudget(tc.args); budget <= tc.step {
			t.Fatalf("driver budget %v does not outlast step deadline %v for %v", budget, tc.step, tc.args)
		}
	}
}

// A failed driver call reports the deadline it ran under, how long it ran and
// whether the deadline ran out; a succeeded call or one without a deadline is
// returned as the driver answered.
func TestDriverResultReportsTheDeadlineOfAFailedCall(t *testing.T) {
	failed := map[string]any{"ok": false, "error": "timed out waiting for selector #ready"}
	for _, tc := range []struct {
		name    string
		value   map[string]any
		args    map[string]any
		elapsed time.Duration
		want    map[string]any
	}{
		{name: "expired", value: failed, args: withTimeout(map[string]any{}, waitForTimeoutMS), elapsed: 30012 * time.Millisecond,
			want: map[string]any{"deadline_ms": float64(waitForTimeoutMS), "elapsed_ms": float64(30012), "deadline_expired": true}},
		{name: "failed early", value: failed, args: withTimeout(map[string]any{}, targetTimeoutMS), elapsed: 40 * time.Millisecond,
			want: map[string]any{"deadline_ms": float64(targetTimeoutMS), "elapsed_ms": float64(40), "deadline_expired": false}},
		{name: "succeeded", value: map[string]any{"ok": true}, args: withTimeout(map[string]any{}, targetTimeoutMS), elapsed: time.Second},
		{name: "no deadline", value: failed, args: map[string]any{}, elapsed: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := driverResult("waitFor", tc.value, tc.args, tc.elapsed)
			testutil.FailErr(t, "encode driver result", err)
			var got map[string]any
			testutil.FailErr(t, "decode driver result", json.Unmarshal(body, &got))
			for _, key := range []string{"deadline_ms", "elapsed_ms", "deadline_expired"} {
				if got[key] != tc.want[key] {
					t.Fatalf("%s = %#v, want %#v; result %s", key, got[key], tc.want[key], body)
				}
			}
			data := actionFailedData(0, CaptureAction{Type: "wait_for", ActionLocator: ActionLocator{Selector: "#ready"}}, body, nil)
			for from, to := range map[string]string{"deadline_ms": "step_timeout_ms", "elapsed_ms": "elapsed_ms", "deadline_expired": "step_deadline_expired"} {
				if data[to] != tc.want[from] {
					t.Fatalf("failure %s = %#v, want %#v", to, data[to], tc.want[from])
				}
			}
		})
	}
}
