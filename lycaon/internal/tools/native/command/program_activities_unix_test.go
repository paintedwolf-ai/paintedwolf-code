//go:build unix

package command_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// A detached job ends after its call returned; the last published snapshot is
// the settled program the receipt reads.
func TestDetachedProgramPublishesItsSettledActivities(t *testing.T) {
	for _, tc := range []struct {
		name  string
		exit  int
		state string
	}{{"clean exit", 0, "completed"}, {"failed exit", 1, "failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProgramFixture(t)
			release := filepath.Join(f.Dir, "release")
			testutil.FailErr(t, "make release fifo", syscall.Mkfifo(release, 0o600))
			snapshots := make(chan api.CommandProgramReceipt, 256)
			f.Configure = func(ctx *tools.ToolContext) {
				ctx.Effects.ReportProgress = func(progress api.ToolProgress) {
					if progress.CommandProgram != nil {
						snapshots <- *progress.CommandProgram
					}
				}
			}
			out, err := f.call(t, t.Context(), fmt.Sprintf("external -- /bin/sh -c 'read _ < release; exit %d' &", tc.exit))
			testutil.FailErr(t, "launch detached job", err)
			outcome := decodeOutcome(t, out)
			if outcome.State != "running" {
				t.Fatalf("call returned a settled program: %s", out)
			}
			writer, err := os.OpenFile(release, os.O_WRONLY, 0)
			testutil.FailErr(t, "open release fifo", err)
			testutil.FailErr(t, "release detached job", writer.Close())

			var last api.CommandProgramReceipt
			for last.State == "" || last.State == "running" || last.State == "waiting" {
				select {
				case last = <-snapshots:
				case <-t.Context().Done():
					t.Fatalf("no settled snapshot; last state %q", last.State)
				}
			}
			settled, err := f.Recorder.ActivitySummary(t.Context(), f.Session, outcome.ProgramID)
			testutil.FailErr(t, "read settled summary", err)
			if !reflect.DeepEqual(last, settled) {
				t.Fatalf("last snapshot differs from the settled receipt:\nlast    %+v\nsettled %+v", last, settled)
			}
			if len(snapshots) != 0 {
				t.Fatalf("%d snapshots followed settlement", len(snapshots))
			}
			var process *api.CommandActivity
			for i := range settled.Activities {
				if settled.Activities[i].ExecutionCount > 0 && settled.Activities[i].State == tc.state {
					process = &settled.Activities[i]
				}
			}
			if process == nil {
				t.Fatalf("no %s activity for exit %d: %+v", tc.state, tc.exit, settled.Activities)
			}
		})
	}
}
