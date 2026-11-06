package conformance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/test/commandconformance"
)

// The status-* corpus cases compare every composition with bash. These cover what
// a reference shell cannot run: the external selector and a stopped job, whose
// shell diagnostic carries a process ID.
func TestCommandEntryStatusesBeyondReferenceShell(t *testing.T) {
	runner := newConformanceRunner(t, gate.PostureLight, true)
	for _, c := range []struct {
		name    string
		program string
		status  int
	}{
		{name: "native into selected external", program: `git status | external -- /bin/cat`, status: 128},
		{name: "selected external into native", program: `external -- /usr/bin/false | cat`, status: 1},
		{name: "selected external into selected external", program: `external -- /usr/bin/false | external -- /bin/cat`, status: 1},
		{name: "bare wait after selected external", program: `external -- /usr/bin/false & wait`},
		{name: "waited selected external", program: `external -- /usr/bin/false & wait $!`, status: 1},
		{name: "bare wait after a stopped job", program: `sleep 30 & kill %1; wait`},
		{name: "waited stopped job", program: `sleep 30 & kill %1; wait %1`, status: 143},
	} {
		t.Run(c.name, func(t *testing.T) {
			obs := runner.runCase(t, commandconformance.Case{ID: "status-" + c.name, Program: c.program})
			if obs.TopLevelReject != "" || obs.Outcome.Refusal != nil {
				t.Fatalf("unexpected refusal: %+v", obs)
			}
			if obs.Outcome.Status != c.status {
				t.Fatalf("status = %d, want %d", obs.Outcome.Status, c.status)
			}
		})
	}
}

// A host refusal is not an ordinary status: it stops the program from a pipeline
// and from either form of wait.
func TestHostRefusalSurvivesPipelineAndWait(t *testing.T) {
	runner := newConformanceRunner(t, gate.PostureLight, true)
	for _, program := range []string{
		`cat /etc/ssh/ssh_host_rsa_key | cat; printf 'unreached\n'`,
		`cat /etc/ssh/ssh_host_rsa_key & wait; printf 'unreached\n'`,
		`cat /etc/ssh/ssh_host_rsa_key & wait $!; printf 'unreached\n'`,
	} {
		t.Run(program, func(t *testing.T) {
			obs := runner.runCase(t, commandconformance.Case{ID: "refusal-" + program, Program: program})
			if obs.Outcome.Refusal == nil || obs.Outcome.Refusal.Code != "SURVEY_PATH_ESCAPE" {
				t.Fatalf("refusal = %+v", obs)
			}
			if obs.Outcome.Status != 2 || obs.Outcome.State != "rejected" || obs.Outcome.Stdout != "" {
				t.Fatalf("outcome = %+v", obs.Outcome)
			}
		})
	}
}
