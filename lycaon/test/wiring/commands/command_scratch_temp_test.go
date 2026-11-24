package commands

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestScratchWorkIsOneChatLocation(t *testing.T) {
	s := newProgramHost(t).program(t, "printf 'draft\\n' > @scratch/notes.md; mkdir -p @scratch/sub; printf 'plan\\n' > @scratch/sub/plan.md; sed -n 1p @scratch/notes.md", "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	if outcome := s.outcome(t, "program-1"); outcome.Status != 0 || outcome.Stdout != "draft\n" {
		t.Fatalf("scratch program = %+v", outcome)
	}
	anchors := s.chatAnchors(t)
	if len(anchors) != 1 || !strings.HasSuffix(anchors[0], "/scratch/"+s.session) {
		t.Fatalf("chat locations = %v, want the session's scratch alone", anchors)
	}
	if changed, read := s.chatFileCounts(t); changed != 2 || read != 1 {
		t.Fatalf("chat files changed=%d read=%d, want 2 and 1", changed, read)
	}
}

func TestProgramTempStaysOutOfHistory(t *testing.T) {
	program := `sh -c 'i=0; while [ $i -lt 3000 ]; do : > "$TMPDIR/t$i"; i=$((i+1)); done; printf kept > "$SCRATCH_DIR/result.txt"; printf "%s" "$TMPDIR"'`
	s := newProgramHost(t).program(t, program, "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	outcome := s.outcome(t, "program-1")
	if outcome.Status != 0 || !strings.HasSuffix(outcome.Stdout, "/scratch/"+s.session+"/.tmp") {
		t.Fatalf("program temp run = %+v", outcome)
	}
	if changed, _ := s.chatFileCounts(t); changed != 1 {
		t.Fatalf("chat files changed = %d, want only the kept result", changed)
	}
	if anchors := s.chatAnchors(t); slices.ContainsFunc(anchors, func(anchor string) bool { return strings.Contains(anchor, "/.tmp") }) {
		t.Fatalf("chat locations = %v include program temp", anchors)
	}
}

func TestApprovedSharedTempRecordsWhatWasTouched(t *testing.T) {
	temp := outsideCommandFolder(t)
	t.Setenv("TMPDIR", temp)
	loose := filepath.Join(temp, "loose.txt")
	folder := filepath.Join(temp, "build")
	program := "printf 'x\\n' > '" + loose + "'; mkdir -p '" + folder + "'; printf 'y\\n' > '" + folder + "/out.txt'; rm '" + loose + "'"
	s := newProgramHost(t, gate.PostureBalanced).program(t, program, "")
	cards := runCommandWithApprovals(t, s, "recommended")
	if plan := cards[0].ToolApproval.Plan; plan.Subject.Kind != wire.ApprovalSubjectKindWriteRootSet {
		t.Fatalf("shared temp review = %+v", plan)
	}
	if outcome := s.outcome(t, "program-1"); outcome.Status != 0 {
		t.Fatalf("shared temp program = %+v", outcome)
	}
	anchors := s.chatAnchors(t)
	want := []string{outsideRel(t, folder), outsideRel(t, loose)}
	slices.Sort(want)
	if !slices.Equal(anchors, want) {
		t.Fatalf("chat locations = %v, want %v", anchors, want)
	}
	if _, err := os.Stat(loose); !os.IsNotExist(err) {
		t.Fatalf("loose file survived its removal: %v", err)
	}
}

func TestSharedTempRootIsReportedNotCompared(t *testing.T) {
	temp := outsideCommandFolder(t)
	t.Setenv("TMPDIR", temp)
	program := `sh -c 'printf z > "` + filepath.Join(temp, "external.txt") + `"'`
	args := map[string]any{"command": program, "capability_request": map[string]any{"write_root": temp}}
	s := newProgramHost(t, gate.PostureBalanced).programWithArgs(t, args, wire.CreateSessionRequest{}, "")
	runCommandWithApprovals(t, s, "recommended")
	if outcome := s.outcome(t, "program-1"); outcome.Status != 0 {
		t.Fatalf("shared temp program = %+v", outcome)
	}
	if changed, _ := s.chatFileCounts(t); changed != 0 {
		t.Fatalf("chat files changed = %d, want the whole temp root left uncompared", changed)
	}
	if !s.windowGap(t, temp, wire.SourceWindowGapSharedTemp) {
		t.Fatalf("no shared temp gap recorded for %s", temp)
	}
}

func (s *commandProgramSession) chatAnchors(t *testing.T) []string {
	t.Helper()
	rows, err := s.h.DB.QueryContext(t.Context(), `SELECT anchor FROM source_chat_locations WHERE session_id = ? ORDER BY anchor`, s.session)
	testutil.FailErr(t, "read chat locations", err)
	defer rows.Close()
	var anchors []string
	for rows.Next() {
		var anchor string
		testutil.FailErr(t, "scan chat location", rows.Scan(&anchor))
		anchors = append(anchors, anchor)
	}
	testutil.FailErr(t, "iterate chat locations", rows.Err())
	return anchors
}

func (s *commandProgramSession) chatFileCounts(t *testing.T) (changed, read int) {
	t.Helper()
	testutil.FailErr(t, "count chat files", s.h.DB.QueryRowContext(t.Context(),
		`SELECT COALESCE(SUM(changed), 0), COALESCE(SUM(read), 0) FROM source_chat_files WHERE session_id = ?`, s.session).Scan(&changed, &read))
	return changed, read
}

// windowGap reports whether a command window recorded folder as not observed for reason.
func (s *commandProgramSession) windowGap(t *testing.T, folder string, reason wire.SourceWindowGapReason) bool {
	t.Helper()
	var count int
	testutil.FailErr(t, "read window coverage", s.h.DB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM source_window_coverage WHERE folder = ? AND reason = ?`, filepath.Clean(folder), string(reason)).Scan(&count))
	return count > 0
}

func outsideRel(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(filepath.Dir(path))
	testutil.FailErr(t, "resolve folder", err)
	return strings.TrimPrefix(filepath.ToSlash(filepath.Join(resolved, filepath.Base(path))), "/")
}
