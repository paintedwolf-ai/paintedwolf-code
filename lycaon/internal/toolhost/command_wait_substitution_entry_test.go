//go:build unix

package toolhost

import (
	"testing"
	"time"
)

func TestCommandEntryOutputSubstitutionWaitsAndPreservesBytes(t *testing.T) {
	for _, tc := range []struct{ name, source, output string }{
		{"writer backpressure", "printf x > >(sleep 120; cat)", "x"},
		{"completion join", "true > >(sleep 120; printf done)", "done"},
		{"ordinary completion", "printf input > >(cat)", "input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWaitStateFixture(t)
			done := f.start(t, tc.source)
			var content string
			if tc.name == "ordinary completion" {
				content = <-done
			} else {
				pending := <-f.parked
				<-done
				f.clock.Advance(119 * time.Second)
				select {
				case early := <-pending.settled:
					t.Fatalf("substitution ended early: %s", early)
				default:
				}
				f.clock.Advance(time.Second)
				content = <-pending.settled
			}
			outcome := decodeWaitOutcome(t, content)
			if outcome.State != "completed" || outcome.Status != 0 || outcome.Stdout != tc.output {
				t.Fatalf("substitution %+v", outcome)
			}
		})
	}
}
