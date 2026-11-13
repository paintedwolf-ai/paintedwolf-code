package bgprocess

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// wireTerminationReasons are every termination reason the wire contract names.
func wireTerminationReasons(t *testing.T) []TerminationReason {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
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
	for _, kind := range []string{processKindPipeline, processKindProgram} {
		for _, observed := range []bool{false, true} {
			add(kind+"-running", &Process{kind: kind, running: true, processRuntimeState: processRuntimeState{observed: observed}}, want{listed: true})
			for _, reason := range wireTerminationReasons(t) {
				exited := reason == TerminationExited && !observed
				add(kind+string(reason)+map[bool]string{true: "-observed", false: ""}[observed],
					&Process{kind: kind, processRuntimeState: processRuntimeState{hasExit: true, exitCode: 3, reason: reason, observed: observed}},
					want{listed: exited, exited: exited})
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
