package workeroutcomes_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
)

// A settled round's notice reaches the terminal envelope; a job without a
// round, or a notice that fails to load, leaves the envelope without one.
func TestTerminalEnvelopeCarriesTheResultNotice(t *testing.T) {
	notice := map[string]any{"worker": "fixer", "result_handle": "result-1"}
	for name, tc := range map[string]struct {
		ok   bool
		err  error
		want any
	}{
		"settled round": {ok: true, want: notice},
		"no round":      {},
		"load failure":  {err: errors.New("store closed")},
	} {
		t.Run(name, func(t *testing.T) {
			results := workeroutcomes.NewResults(workeroutcomes.ResultPorts{Digests: workeroutcomes.NewDigests()})
			var asked string
			results.SetResultNotices(func(_ context.Context, jobID string) (map[string]any, bool, error) {
				asked = jobID
				return notice, tc.ok, tc.err
			})
			env := results.EnvelopeForTerminal(t.Context(), "coord", "job-1")
			if asked != "job-1" {
				t.Fatalf("notice asked for %q", asked)
			}
			if got := env.Vars["worker_result"]; !reflect.DeepEqual(got, tc.want) && !(tc.want == nil && got == nil) {
				t.Fatalf("worker_result = %#v, want %#v", got, tc.want)
			}
		})
	}
}
