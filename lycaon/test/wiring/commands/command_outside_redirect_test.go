package commands

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestOutsideOutputRedirectAsksLikeANativeWrite(t *testing.T) {
	for _, declared := range []bool{false, true} {
		t.Run(map[bool]string{false: "undeclared", true: "declared"}[declared], func(t *testing.T) {
			outside := outsideCommandFolder(t)
			target := filepath.Join(outside, "test-file.txt")
			args := map[string]any{"command": "printf 'hello\\n' > '" + target + "' && cat -n '" + target + "'"}
			if declared {
				args["capability_request"] = map[string]any{"write_root": target}
			}
			s := newProgramHost(t, gate.PostureBalanced).programWithArgs(t, args, wire.CreateSessionRequest{}, "")
			cards := runCommandWithApprovals(t, s, "recommended")
			plan := cards[0].ToolApproval.Plan
			if plan.Subject.Kind != wire.ApprovalSubjectKindWriteRootSet || !slices.Contains(plan.Reasons, wire.GateOutsideRootsWrite) {
				t.Fatalf("redirect review = %+v", plan)
			}
			wantRoot := outside
			if declared {
				wantRoot = target
			}
			if len(plan.Subject.Targets) != 1 || plan.Subject.Targets[0].Label != wantRoot {
				t.Fatalf("redirect review targets = %+v, want %s", plan.Subject.Targets, wantRoot)
			}
			data, err := os.ReadFile(target)
			testutil.FailErr(t, "read redirect target", err)
			if string(data) != "hello\n" {
				t.Fatalf("redirect target = %q", data)
			}
			outcome := s.outcome(t, "program-1")
			if outcome.Status != 0 || !strings.Contains(outcome.Stdout, "hello") {
				t.Fatalf("approved redirect = %+v", outcome)
			}
			records := s.outsideRecords(t)
			if records.changed != 1 || !slices.Contains(records.anchors, outsideAnchor(t, outside)) {
				t.Fatalf("outside records = %+v, want one change under %s", records, outside)
			}
			if !s.navigatesToFile(t, "program-1", target) {
				t.Fatal("redirect result carries no file navigation target")
			}
		})
	}
}

func TestDeclinedOutsideOutputRedirectWritesNothing(t *testing.T) {
	outside := outsideCommandFolder(t)
	target := filepath.Join(outside, "declined.txt")
	s := newProgramHost(t, gate.PostureBalanced).program(t, "printf 'hello\\n' > '"+target+"'", "")
	runCommandWithApprovals(t, s, "decline")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("declined redirect wrote its target: %v", err)
	}
	outcome := s.outcome(t, "program-1")
	if outcome.Refusal == nil || outcome.Refusal.Code != "SANDBOX_WRITE_ROOT_DENIED" {
		t.Fatalf("declined redirect refusal = %+v", outcome.Refusal)
	}
}

func TestOutsideInputRedirectAndSourceAskForRead(t *testing.T) {
	for _, tc := range []struct{ name, file, content, command, want string }{
		{"input", "private.txt", "one\ntwo\n", "wc -l < 'FILE'", "2"},
		{"source", "env.sh", "GREETING=sourced\n", "source 'FILE'; printf '%s\\n' \"$GREETING\"", "sourced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outside := outsideCommandFolder(t)
			file := filepath.Join(outside, tc.file)
			testutil.FailErr(t, "seed outside input", os.WriteFile(file, []byte(tc.content), 0o600))
			s := newProgramHost(t, gate.PostureBalanced).program(t, strings.ReplaceAll(tc.command, "FILE", file), "")
			cards := runCommandWithApprovals(t, s, "recommended")
			if plan := cards[0].ToolApproval.Plan; !slices.Contains(plan.Reasons, wire.GateOutsideRootsRead) {
				t.Fatalf("input review = %+v", plan)
			}
			outcome := s.outcome(t, "program-1")
			if outcome.Status != 0 || strings.TrimSpace(outcome.Stdout) != tc.want {
				t.Fatalf("reviewed input = %+v", outcome)
			}
		})
	}
}

func TestDeclinedOutsideInputRedirectDeliversNothing(t *testing.T) {
	outside := outsideCommandFolder(t)
	file := filepath.Join(outside, "private.txt")
	testutil.FailErr(t, "seed outside input", os.WriteFile(file, []byte("secret\n"), 0o600))
	s := newProgramHost(t, gate.PostureBalanced).program(t, "cat < '"+file+"'", "")
	runCommandWithApprovals(t, s, "decline")
	outcome := s.outcome(t, "program-1")
	if outcome.Refusal == nil || outcome.Refusal.Code == "" || outcome.Status == 0 || strings.Contains(outcome.Stdout, "secret") {
		t.Fatalf("declined input = %+v", outcome)
	}
}

type outsideRecords struct {
	anchors []string
	changed int
}

// outsideRecords reads the chat's outside file references and locations.
func (s *commandProgramSession) outsideRecords(t *testing.T) outsideRecords {
	t.Helper()
	var out outsideRecords
	testutil.FailErr(t, "count outside files", s.h.DB.QueryRowContext(t.Context(),
		`SELECT COALESCE(SUM(changed), 0) FROM source_chat_files WHERE session_id = ?`, s.session).Scan(&out.changed))
	rows, err := s.h.DB.QueryContext(t.Context(), `SELECT anchor FROM source_chat_locations WHERE session_id = ? ORDER BY anchor`, s.session)
	testutil.FailErr(t, "read chat locations", err)
	defer rows.Close()
	for rows.Next() {
		var anchor string
		testutil.FailErr(t, "scan chat location", rows.Scan(&anchor))
		out.anchors = append(out.anchors, anchor)
	}
	testutil.FailErr(t, "iterate chat locations", rows.Err())
	return out
}

// outsideAnchor is how a chat location names a host folder.
func outsideAnchor(t *testing.T, folder string) string {
	t.Helper()
	canonical := fspath.CanonicalPath(folder)
	if canonical == "" {
		t.Fatalf("canonical path of %s", folder)
	}
	return strings.TrimPrefix(filepath.ToSlash(canonical), "/")
}

// navigatesToFile reports whether a tool result links file by its file ID.
func (s *commandProgramSession) navigatesToFile(t *testing.T, callID, file string) bool {
	t.Helper()
	msgs, err := s.h.Store.Transcript.GetMessages(t.Context(), s.session)
	testutil.FailErr(t, "read transcript", err)
	want := outsideAnchor(t, file)
	for _, msg := range msgs {
		if msg.ToolResult == nil || msg.ToolResult.ToolCallID != callID || msg.SourceContext == nil {
			continue
		}
		for _, location := range msg.SourceContext.Locations {
			if location.FileID != "" && location.Path == want {
				return true
			}
		}
	}
	return false
}
