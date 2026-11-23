package bgprocess

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

// wireTerminationReasons are every termination reason the wire contract names.
func wireTerminationReasons(t *testing.T) []TerminationReason {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "openapi.yaml"))
	testutil.FailErr(t, "read the wire contract", err)
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	testutil.FailErr(t, "parse the wire contract", yaml.Unmarshal(raw, &spec))
	var reasons []TerminationReason
	for _, reason := range spec.Components.Schemas["BackgroundProcessTerminationReason"].Enum {
		reasons = append(reasons, TerminationReason(reason))
	}
	if len(reasons) == 0 {
		t.Fatal("wire contract names no termination reasons")
	}
	return reasons
}

// For every way a command job can end, observed or not, and while it runs:
// the ledger lists a running job, lists an ended job only when it exited on
// its own and the agent has not observed it, and never lists a silent job or
// a terminal.
func TestLedgerListsExactlyRunningAndUnobservedExits(t *testing.T) {
	r := NewRegistry(DefaultConfig(), Hooks{})
	type want struct{ listed, exited bool }
	expected := map[string]want{}
	session := r.jobs.sessions
	session["s"] = map[string]*Process{}
	add := func(handle string, proc *Process, w want) {
		proc.Handle, proc.SessionID = handle, "s"
		proc.startedAt = time.Unix(int64(len(expected)), 0)
		session["s"][handle] = proc
		expected[handle] = w
	}
	reasons := wireTerminationReasons(t)
	for _, kind := range []string{processKindPipeline, processKindProgram} {
		add(kind+"-running", &Process{kind: kind, running: true}, want{listed: true})
		for _, observed := range []bool{false, true} {
			for _, reason := range reasons {
				exited := reason == TerminationExited && !observed
				handle := kind + string(reason) + map[bool]string{true: "-observed", false: ""}[observed]
				add(handle, &Process{kind: kind, processRuntimeState: processRuntimeState{hasExit: true, exitCode: 3, reason: reason}},
					want{listed: exited, exited: exited})
				if observed {
					r.Observe("s", handle)
				}
			}
		}
		add(kind+"-silent", &Process{kind: kind, running: true, silent: true}, want{})
	}
	add("terminal", &Process{kind: processKindPTY, running: true}, want{})

	listed := map[string]JobSnapshot{}
	for _, job := range r.LedgerJobs("s") {
		listed[job.Handle] = job
	}
	for handle, w := range expected {
		job, ok := listed[handle]
		if ok != w.listed || job.Exited != w.exited || (w.exited && job.ExitCode != 3) {
			t.Errorf("%s: listed=%v %+v, want %+v", handle, ok, job, w)
		}
	}
}
