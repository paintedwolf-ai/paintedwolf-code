package wiring

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestWorkflowReadOnlyFileRefusesNativePublication(t *testing.T) {
	for _, program := range []string{
		"printf replacement > locked.txt",
		"printf replacement | tee locked.txt",
		"sed -i 's/original/replacement/' locked.txt",
	} {
		t.Run(program, func(t *testing.T) {
			f := newCommandWorkflowFixture(t, []workflowTurn{
				{callID: "read-locked", command: "cat locked.txt", inspect: func(t *testing.T, result command.ProgramOutcome) {
					if result.Status != 0 || result.Stdout != "original\n" {
						t.Fatalf("read-only source was not readable: %+v", result)
					}
				}},
				{callID: "write-locked", command: program},
			})
			f.seed(t, "locked.txt", "original\n")
			path := filepath.Join(f.dir, "locked.txt")
			testutil.FailErr(t, "make source read-only", os.Chmod(path, 0o444))
			t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
			f.run(t)
			result := f.outcome(t, "write-locked")
			if result.Status == 0 || result.Refusal == nil || result.Refusal.Code == "" {
				t.Errorf("read-only publication did not report a typed refusal: %+v", result)
			}
			if actual := f.readFile(t, "locked.txt"); actual != "original\n" {
				t.Errorf("read-only source changed to %q", actual)
			}
			info, err := os.Stat(path)
			testutil.FailErr(t, "inspect retained permissions", err)
			if info.Mode().Perm() != 0o444 {
				t.Errorf("read-only mode changed to %o", info.Mode().Perm())
			}
		})
	}
}

func TestCommandProgramNestedNativeHelpKeepsItsOwner(t *testing.T) {
	f := newCommandWorkflowFixture(t, []workflowTurn{{callID: "nested-help", command: "env git status --help"}})
	f.run(t)
	result := f.outcome(t, "nested-help")
	if result.Status != 0 || result.Refusal != nil || result.Stdout == "" {
		t.Fatalf("native help did not run through env: %+v", result)
	}
}
