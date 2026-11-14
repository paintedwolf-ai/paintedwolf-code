package command_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// $! is the shell's numeric job ID; receipts carry the registry handle the
// host and Den address; the registry names the job by its statement.
func TestBackgroundJobAddressesMatchTheirReaders(t *testing.T) {
	for _, tc := range []struct{ name, statement string }{
		{"external", "external -- /bin/sleep 30 &"},
		{"native", "sleep 30 &"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProgramFixture(t)
			out, err := f.call(t, t.Context(), tc.statement+"\nid=$!\nkill $id\nwait $id\necho \"$id $?\"")
			testutil.FailErr(t, "run job and address it by $!", err)
			outcome := decodeOutcome(t, out)
			fields := strings.Fields(outcome.Stdout)
			if len(fields) != 2 {
				t.Fatalf("stdout %q", outcome.Stdout)
			}
			if id, err := strconv.Atoi(fields[0]); err != nil || id <= 0 {
				t.Fatalf("$! = %q, want a positive integer", fields[0])
			}
			if status, err := strconv.Atoi(fields[1]); err != nil || status <= 128 {
				t.Fatalf("wait $! status = %q, want the killed job's signal status", fields[1])
			}

			page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 100)
			testutil.FailErr(t, "read program receipt", err)
			var handle, launch string
			for _, execution := range page.Executions {
				if execution.JobHandle != "" {
					handle, launch = execution.JobHandle, execution.ID
				}
			}
			if known, _ := f.Background.State(f.Session, handle); !known {
				t.Fatalf("receipt job handle %q is unknown to the registry", handle)
			}
			commandLine, err := f.Background.CommandLine(f.Session, handle)
			testutil.FailErr(t, "read job command line", err)
			if commandLine != tc.statement {
				t.Fatalf("job command line = %q, want %q", commandLine, tc.statement)
			}

			var activity string
			for _, candidate := range page.Activities {
				if candidate.JobHandle == handle {
					activity = candidate.ID
				}
			}
			scoped, err := f.Recorder.ReadProgramActivityPage(t.Context(), f.Session, outcome.ProgramID, activity, "", 100)
			testutil.FailErr(t, "read launch activity", err)
			var jobWork bool
			for _, execution := range scoped.Executions {
				jobWork = jobWork || execution.ID != launch
			}
			if !jobWork {
				t.Fatalf("launch activity omits its job's executions: %+v", scoped.Executions)
			}
		})
	}
}
