package wiring

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type linkProgramReceipt struct {
	state, stopCode, stdout                              string
	status, children, completedEffects, uncertainEffects int
}

func linkProgramOutcome(t *testing.T, session *commandProgramSession) linkProgramReceipt {
	t.Helper()
	var out linkProgramReceipt
	testutil.FailErr(t, "read program receipt", session.h.DB.QueryRowContext(t.Context(), `SELECT p.state, p.status_code, p.stop_code,
		(SELECT count(*) FROM invocation_program_executions e WHERE e.program_id = p.id),
		(SELECT count(*) FROM invocation_program_executions e WHERE e.program_id = p.id AND e.effect_certainty = 'completed'),
		(SELECT count(*) FROM invocation_program_executions e WHERE e.program_id = p.id AND e.effect_certainty = 'unknown')
		FROM invocation_programs p JOIN invocation_receipts r ON r.id = p.parent_receipt_id WHERE r.session_id = ?`, session.session).
		Scan(&out.state, &out.status, &out.stopCode, &out.children, &out.completedEffects, &out.uncertainEffects))
	rows, err := session.h.DB.QueryContext(t.Context(), `SELECT o.preview FROM invocation_program_outputs o
		JOIN invocation_programs p ON p.id = o.program_id JOIN invocation_receipts r ON r.id = p.parent_receipt_id
		WHERE r.session_id = ? AND o.stream = 'stdout' ORDER BY o.created_at, o.id`, session.session)
	testutil.FailErr(t, "read output receipts", err)
	defer rows.Close()
	for rows.Next() {
		var preview string
		testutil.FailErr(t, "scan output receipt", rows.Scan(&preview))
		out.stdout += preview
	}
	testutil.FailErr(t, "iterate output receipts", rows.Err())
	return out
}

func TestCommandProgramSymbolicLinksThroughProvider(t *testing.T) {
	t.Run("create read and replace", func(t *testing.T) {
		s := newCommandProgramSession(t, "ln -s notes.txt link.txt; cat link.txt; ln -sf sub/replacement.txt link.txt; cat link.txt", "")
		testutil.FailErr(t, "seed replacement", os.WriteFile(filepath.Join(s.dir, "sub", "replacement.txt"), []byte("replacement note\n"), 0o644))
		testutil.FailErr(t, "prompt", s.prompt(t.Context()))
		outcome := linkProgramOutcome(t, s)
		if outcome.state != "completed" || outcome.status != 0 || outcome.stdout != "first note\nsecond note\nreplacement note\n" ||
			outcome.completedEffects != 2 || outcome.uncertainEffects != 0 || outcome.children != 4 {
			t.Fatalf("link program outcome: %+v", outcome)
		}
		if got, err := os.Readlink(filepath.Join(s.dir, "link.txt")); err != nil || got != "sub/replacement.txt" {
			t.Fatalf("stored link text = %q, %v", got, err)
		}
		if got := s.executions(t); len(got) != 4 || got[0] != "write=completed" || got[1] != "read=completed" ||
			got[2] != "write=completed" || got[3] != "read=completed" {
			t.Fatalf("child receipts = %v", got)
		}
	})

	for _, tc := range []struct {
		name, program, code, link string
	}{
		{"hard link", "ln notes.txt hard.txt", "COMMAND_NATIVE_FORM_UNSUPPORTED", "hard.txt"},
		{"outside target", "ln -s ../outside escaped.txt", "SURVEY_PATH_ESCAPE", "escaped.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCommandProgramSession(t, tc.program, "")
			testutil.FailErr(t, "prompt", s.prompt(t.Context()))
			outcome := linkProgramOutcome(t, s)
			if outcome.state != "rejected" || outcome.status == 0 || outcome.completedEffects != 0 || outcome.uncertainEffects != 0 ||
				outcome.stopCode != tc.code || outcome.children != 1 {
				t.Fatalf("refused link outcome: %+v", outcome)
			}
			if _, err := os.Lstat(filepath.Join(s.dir, tc.link)); !os.IsNotExist(err) {
				t.Fatalf("refused link exists: %v", err)
			}
			got := s.executions(t)
			if len(got) != 1 || got[0] != "write=refused" {
				t.Fatalf("refused child receipt = %v", got)
			}
		})
	}
}
