package commands

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestNamedOutsideReadsAreRecorded(t *testing.T) {
	outside := outsideCommandFolder(t)
	file := filepath.Join(outside, "notes.txt")
	testutil.FailErr(t, "seed outside file", os.WriteFile(file, []byte("needle\nhay\n"), 0o600))
	for _, program := range []string{
		"cat '" + file + "'",
		"head -n 1 '" + file + "'",
		"rg needle '" + file + "'",
	} {
		t.Run(strings.Fields(program)[0], func(t *testing.T) {
			s := newProgramHost(t, gate.PostureBalanced).program(t, program, "")
			runCommandApprovingAll(t, s)
			if outcome := s.outcome(t, "program-1"); outcome.Status != 0 || !strings.Contains(outcome.Stdout, "needle") {
				t.Fatalf("outside read = %+v", outcome)
			}
			if read := s.readFiles(t); read != 1 {
				t.Fatalf("chat file reads = %d, want 1", read)
			}
			if anchors := s.locationAnchors(t); !slices.Contains(anchors, outsideAnchorOf(t, outside)) {
				t.Fatalf("chat locations = %v, want %s", anchors, outside)
			}
		})
	}
}

func TestCurlFileBodyIsARecordedRead(t *testing.T) {
	outside := outsideCommandFolder(t)
	file := filepath.Join(outside, "payload.json")
	testutil.FailErr(t, "seed payload", os.WriteFile(file, []byte(`{"ok":true}`), 0o600))
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse listener", err)
	port, err := strconv.ParseUint(target.Port(), 10, 16)
	testutil.FailErr(t, "listener port", err)
	s := newProgramHost(t, gate.PostureBalanced).program(t, "curl -s -d @'"+file+"' '"+server.URL+"'", "")
	if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, []uint16{uint16(port)}, "curl-listener", "curl-fixture", nil) {
		t.Fatal("listener grant failed")
	}
	runCommandApprovingAll(t, s)
	if outcome := s.outcome(t, "program-1"); outcome.Status != 0 || received != `{"ok":true}` {
		t.Fatalf("curl upload = %+v, server received %q", outcome, received)
	}
	if read := s.readFiles(t); read != 1 {
		t.Fatalf("chat file reads = %d, want 1", read)
	}
}

func TestWalkedSearchRecordsOnlyMatchingFiles(t *testing.T) {
	outside := outsideCommandFolder(t)
	for i := range 200 {
		content := "hay\n"
		if i == 17 || i == 160 {
			content = "needle\n"
		}
		testutil.FailErr(t, "seed walked file", os.WriteFile(filepath.Join(outside, fmt.Sprintf("f%03d.txt", i)), []byte(content), 0o600))
	}
	s := newProgramHost(t, gate.PostureBalanced).program(t, "rg -l needle '"+outside+"'", "")
	runCommandApprovingAll(t, s)
	if outcome := s.outcome(t, "program-1"); outcome.Status != 0 || strings.Count(outcome.Stdout, "\n") != 2 {
		t.Fatalf("walked search = %+v", outcome)
	}
	if read := s.readFiles(t); read != 2 {
		t.Fatalf("chat file reads = %d, want only the 2 matching files", read)
	}
}

// runCommandApprovingAll runs the program, answering every approval card with
// its recommended option.
func runCommandApprovingAll(t *testing.T, s *commandProgramSession) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.prompt(t.Context()) }()
	kind := wire.CheckpointKindToolApproval
	var runErr error
	testutil.WaitFor(t, "the command ends, approving each card", func() bool {
		select {
		case runErr = <-done:
			return true
		default:
		}
		pending, err := s.h.Sessions.Checkpoints.ListPending(t.Context(), s.session, &kind)
		if err != nil {
			return false
		}
		for _, checkpoint := range pending {
			_, err := s.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(s.h.OwnerCtx(t, t.Context()), s.session, checkpoint.ID, checkpoint.ToolApproval.Plan.RecommendedOptionID)
			testutil.FailErr(t, "answer approval", err)
		}
		return false
	})
	testutil.FailErr(t, "complete command", runErr)
}

func (s *commandProgramSession) readFiles(t *testing.T) int {
	t.Helper()
	var read int
	testutil.FailErr(t, "count read files", s.h.DB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM source_chat_files WHERE session_id = ? AND read = 1`, s.session).Scan(&read))
	return read
}

func (s *commandProgramSession) locationAnchors(t *testing.T) []string {
	t.Helper()
	rows, err := s.h.DB.QueryContext(t.Context(), `SELECT anchor FROM source_chat_locations WHERE session_id = ?`, s.session)
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

func outsideAnchorOf(t *testing.T, folder string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(folder)
	testutil.FailErr(t, "resolve folder", err)
	return strings.TrimPrefix(filepath.ToSlash(resolved), "/")
}
