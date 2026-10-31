package wiring

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWorkflowReadOnlyFileRefusesNativePublication(t *testing.T) {
	cases := []struct {
		name    string
		program string
		wantOp  string
	}{
		{name: "redirect", program: "printf replacement > locked.txt", wantOp: "write"},
		{name: "tee", program: "printf replacement | tee locked.txt", wantOp: "write"},
		{name: "sed in place", program: "sed -i 's/original/replacement/' locked.txt", wantOp: "sed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newCommandProgramSession(t, c.program, "")
			locked := filepath.Join(s.dir, "locked.txt")
			testutil.FailErr(t, "seed locked file", os.WriteFile(locked, []byte("original\n"), 0o644))
			testutil.FailErr(t, "lock file", os.Chmod(locked, 0o444))
			t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

			testutil.FailErr(t, "prompt", s.prompt(t.Context()))
			raw := s.result(t)
			for _, want := range []string{
				`"state": "rejected"`,
				`"stop_code": "COMMAND_OPERAND_INVALID"`,
				`"code": "COMMAND_OPERAND_INVALID"`,
				`destination is read-only`,
			} {
				if !strings.Contains(raw, want) {
					t.Fatalf("read-only publication result missing %q: %s", want, raw)
				}
			}
			if state := s.programState(t); state != "rejected" {
				t.Fatalf("program state = %q, want rejected", state)
			}
			execs := s.executions(t)
			found := false
			for _, row := range execs {
				if row == c.wantOp+"=refused" || row == c.wantOp+"=rejected" {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("child receipts %v lack %s=refused/rejected", execs, c.wantOp)
			}
			got, err := os.ReadFile(locked)
			testutil.FailErr(t, "read locked file", err)
			if string(got) != "original\n" {
				t.Fatalf("read-only file mutated to %q", string(got))
			}
		})
	}
}
