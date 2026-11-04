package tools

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAwaitClosingWindows(t *testing.T) {
	never := make(chan struct{})
	settled := make(chan struct{})
	close(settled)
	running := sourceledger.SessionWindow{ID: "running", Settled: never}
	closing := sourceledger.SessionWindow{ID: "closing", Closing: true, Settled: never}
	done := sourceledger.SessionWindow{ID: "done", Closing: true, Settled: settled}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	for name, run := range map[string]func(){
		"a running window is never waited on": func() {
			AwaitClosingWindows(t.Context(), []sourceledger.SessionWindow{running, done}, time.Hour)
		},
		"a window that stays closing costs the budget": func() {
			AwaitClosingWindows(t.Context(), []sourceledger.SessionWindow{closing}, 0)
		},
		"the caller's context ends the wait": func() {
			AwaitClosingWindows(canceled, []sourceledger.SessionWindow{closing}, time.Hour)
		},
	} {
		t.Run(name, func(t *testing.T) {
			returned := make(chan struct{})
			go func() {
				defer close(returned)
				run()
			}()
			testutil.AwaitClosed(t, name, returned)
		})
	}
}
