package conformance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/test/commandconformance"
)

// Each status is what bash -o pipefail reports for the program in a tree with no
// repository. The reference starts with pipefail because the language always has
// it, so a failing producer fails its pipeline whichever owner ran each stage.
func TestPipelineAndJobStatusesMatchReferenceShell(t *testing.T) {
	runner := newConformanceRunner(t, gate.PostureLight, true)
	for _, c := range []struct {
		name    string
		program string
		status  int
		stdout  string
	}{
		{name: "native git into native", program: `git status | cat`, status: 128},
		{name: "native builtin into native", program: `false | cat`, status: 1},
		{name: "native utility into native", program: `cat missing.txt | cat`, status: 1},
		{name: "native git into external", program: `git status | external -- /bin/cat`, status: 128},
		{name: "native utility into external", program: `cat missing.txt | external -- /bin/cat`, status: 1},
		{name: "external into native", program: `external -- /usr/bin/false | cat`, status: 1},
		{name: "external into external", program: `external -- /usr/bin/false | external -- /bin/cat`, status: 1},
		{name: "pipe statuses keep each stage", program: `false | true; printf '%s\n' "${PIPESTATUS[*]}"`, stdout: "1 0\n"},

		{name: "bare wait after native git", program: `git status & wait`},
		{name: "bare wait after native builtin", program: `false & wait`},
		{name: "bare wait after native utility", program: `cat missing.txt & wait`},
		{name: "bare wait after external", program: `external -- /usr/bin/false & wait`},
		{name: "bare wait after a stopped job", program: `sleep 30 & kill %1; wait`},
		{name: "bare wait sets the next status", program: `false & wait; printf 'after:%s\n' "$?"`, stdout: "after:0\n"},
		{name: "bare wait with no jobs", program: `wait`},

		{name: "waited pid after native git", program: `git status & wait $!`, status: 128},
		{name: "waited job after native git", program: `git status & wait %1`, status: 128},
		{name: "waited pid after native builtin", program: `false & wait $!`, status: 1},
		{name: "waited pid after external", program: `external -- /usr/bin/false & wait $!`, status: 1},
		{name: "last waited operand decides", program: `true & a=$!; false & b=$!; wait "$a" "$b"; printf '%s\n' "$?"; true & a=$!; false & b=$!; wait "$b" "$a"; printf '%s\n' "$?"`, stdout: "1\n0\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			obs := runner.runCase(t, commandconformance.Case{ID: "status-" + c.name, Program: c.program})
			if obs.TopLevelReject != "" || obs.Outcome.Refusal != nil {
				t.Fatalf("unexpected refusal: %+v", obs)
			}
			if obs.Outcome.Status != c.status || obs.Outcome.Stdout != c.stdout {
				t.Fatalf("status = %d stdout = %q, want %d %q", obs.Outcome.Status, obs.Outcome.Stdout, c.status, c.stdout)
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
