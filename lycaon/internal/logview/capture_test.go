package logview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPreferCaptureFileSessionWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionDir := filepath.Join(home, ".config", "paintedwolf", "debug", "sessions", "20260622T203110Z")
	activeDir := filepath.Join(home, ".config", "paintedwolf", "debug", "sessions", "20260622T210000Z")
	mustWrite(t, filepath.Join(sessionDir, "den-perf.jsonl"), `{"ts":"2026-06-22T20:31:10Z","event":"loop-stall","detail":{"lag_ms":100}}`+"\n")
	mustWrite(t, filepath.Join(activeDir, "den-perf.jsonl"), `{"ts":"2026-06-22T20:31:10Z","event":"loop-stall","detail":{"lag_ms":999}}`+"\n")
	t.Setenv(debugpaths.SessionDirEnv, activeDir)

	c := newCapture(sessionDir)
	if c.DenPerfPath != filepath.Join(sessionDir, "den-perf.jsonl") {
		t.Fatalf("session file should win: got %s", c.DenPerfPath)
	}
	recs, err := c.DenPerf()
	testutil.FailErr(t, "c.DenPerf failed", err)
	if len(recs) != 1 || recs[0].StallMS() != 100 {
		t.Fatalf("loaded session stalls = %+v", recs)
	}
}

// A named session that never enabled a stream still shows the live run's copy
// of it, so an older capture can be inspected next to what the sidecar is
// writing now.
func TestPreferCaptureFileFallsBackToTheActiveRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionDir := filepath.Join(home, ".config", "paintedwolf", "debug", "sessions", "20260622T203110Z")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	mustWrite(t, filepath.Join(sessionDir, "sessions.jsonl"), `{"ts":"2026-06-22T20:31:10Z","session_id":"c","agent_type":"coordinator","task":"hi"}`+"\n")
	activeDir := filepath.Join(home, ".config", "paintedwolf", "debug", "sessions", "20260622T210000Z")
	mustWrite(t, filepath.Join(activeDir, "den-perf.jsonl"),
		`{"ts":"2026-06-22T21:02:00Z","event":"loop-stall","detail":{"lag_ms":748,"recent":"thumbnail.capture:start"}}`+"\n")
	t.Setenv(debugpaths.SessionDirEnv, activeDir)

	c := newCapture(sessionDir)
	want := filepath.Join(activeDir, "den-perf.jsonl")
	if c.DenPerfPath != want {
		t.Fatalf("DenPerfPath = %s, want the active run's file %s", c.DenPerfPath, want)
	}
	recs, err := c.DenPerf()
	testutil.FailErr(t, "c.DenPerf failed", err)
	if len(recs) != 1 || !recs[0].IsStall() || recs[0].StallMS() != 748 {
		t.Fatalf("active-run den-perf = %+v", recs)
	}
}

// A viewer process has no session dir of its own; a stream absent from the
// named session resolves to that session's path, which then reads as empty.
func TestPreferCaptureFileWithoutAnActiveRunStaysInTheSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(debugpaths.SessionDirEnv, "")
	t.Setenv("LYCAON_DEN_PERF_DEBUG_FILE", "")

	sessionDir := filepath.Join(home, ".config", "paintedwolf", "debug", "sessions", "20260622T203110Z")
	mustWrite(t, filepath.Join(sessionDir, "sessions.jsonl"), `{"ts":"2026-06-22T20:31:10Z","session_id":"c","agent_type":"coordinator","task":"hi"}`+"\n")

	c := newCapture(sessionDir)
	if want := filepath.Join(sessionDir, "den-perf.jsonl"); c.DenPerfPath != want {
		t.Fatalf("DenPerfPath = %s, want %s", c.DenPerfPath, want)
	}
	recs, err := c.DenPerf()
	testutil.FailErr(t, "c.DenPerf failed", err)
	if len(recs) != 0 {
		t.Fatalf("absent stream produced records: %+v", recs)
	}
}

// A LYCAON_*_FILE redirect is where the writer is writing, so the viewer
// follows it.
func TestPreferCaptureFileFollowsWriterRedirect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionDir := filepath.Join(home, ".config", "paintedwolf", "debug", "sessions", "20260622T203110Z")
	mustWrite(t, filepath.Join(sessionDir, "sessions.jsonl"),
		`{"ts":"2026-06-22T20:31:10Z","session_id":"c","agent_type":"coordinator","task":"hi"}`+"\n")

	redirect := filepath.Join(t.TempDir(), "den-perf.jsonl")
	mustWrite(t, redirect, `{"ts":"2026-06-22T20:32:00Z","event":"loop-stall","detail":{"lag_ms":512}}`+"\n")
	t.Setenv("LYCAON_DEN_PERF_DEBUG_FILE", redirect)

	c := newCapture(sessionDir)
	if c.DenPerfPath != redirect {
		t.Fatalf("DenPerfPath = %s, want the redirect target %s", c.DenPerfPath, redirect)
	}
	recs, err := c.DenPerf()
	testutil.FailErr(t, "c.DenPerf failed", err)
	if len(recs) != 1 || recs[0].StallMS() != 512 {
		t.Fatalf("redirected den-perf = %+v", recs)
	}
}

// A redirect must not hijack a capture the operator named: `logs --dir <old>`
// keeps showing that session's own copy of a stream.
func TestSessionFileStillWinsOverRedirect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionDir := filepath.Join(home, ".config", "paintedwolf", "debug", "sessions", "20260622T203110Z")
	mustWrite(t, filepath.Join(sessionDir, "den-perf.jsonl"),
		`{"ts":"2026-06-22T20:31:10Z","event":"loop-stall","detail":{"lag_ms":100}}`+"\n")

	redirect := filepath.Join(t.TempDir(), "den-perf.jsonl")
	mustWrite(t, redirect, `{"ts":"2026-06-22T21:00:00Z","event":"loop-stall","detail":{"lag_ms":999}}`+"\n")
	t.Setenv("LYCAON_DEN_PERF_DEBUG_FILE", redirect)

	c := newCapture(sessionDir)
	if want := filepath.Join(sessionDir, "den-perf.jsonl"); c.DenPerfPath != want {
		t.Fatalf("DenPerfPath = %s, want the named session's file %s", c.DenPerfPath, want)
	}
}

// With no session dir and nothing at the config root, the redirect target is the
// only capture there is — reporting "no capture found" would be wrong.
func TestResolveFindsRedirectDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_DEBUG_SESSION_DIR", "")

	redirectDir := t.TempDir()
	mustWrite(t, filepath.Join(redirectDir, "llm-requests.jsonl"),
		`{"ts":"2026-06-22T20:31:10Z","provider_id":"mock","model":"m","call":"chat","messages":[]}`+"\n")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", filepath.Join(redirectDir, "llm-requests.jsonl"))

	c, err := Resolve("")
	testutil.FailErr(t, "Resolve failed", err)
	if c.Dir != redirectDir {
		t.Fatalf("Dir = %s, want redirect dir %s", c.Dir, redirectDir)
	}
	rows, err := c.LLM()
	testutil.FailErr(t, "c.LLM failed", err)
	if len(rows) != 1 {
		t.Fatalf("redirected llm rows = %d, want 1", len(rows))
	}
}

// With nothing captured anywhere, resolution says so instead of inventing a
// location under the config root.
func TestResolveReportsNoCaptureWhenNothingWasWritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(debugpaths.SessionDirEnv, "")
	for _, f := range debugpaths.Files() {
		if f.FileEnv != "" {
			t.Setenv(f.FileEnv, "")
		}
	}

	if _, err := Resolve(""); err == nil {
		t.Fatal("Resolve found a capture in an empty config root")
	}
}

func TestListCaptureSummariesCarriesStallBadge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(debugpaths.SessionDirEnv, "")

	sessionDir := filepath.Join(home, ".config", "paintedwolf", "debug", "sessions", "20260622T203110Z")
	mustWrite(t, filepath.Join(sessionDir, "sessions.jsonl"),
		`{"ts":"2026-06-22T20:31:10Z","session_id":"c","agent_type":"coordinator","task":"Tell me about this repo"}`+"\n")
	mustWrite(t, filepath.Join(sessionDir, "den-perf.jsonl"),
		`{"ts":"2026-06-22T20:32:00Z","event":"loop-stall","detail":{"lag_ms":840}}`+"\n"+
			`{"ts":"2026-06-22T20:32:01Z","event":"sync","detail":{"dur_ms":10}}`+"\n")

	sums, err := ListCaptureSummaries()
	testutil.FailErr(t, "ListCaptureSummaries failed", err)
	if len(sums) != 1 {
		t.Fatalf("summaries = %d, want the one session", len(sums))
	}
	if sums[0].When.IsZero() {
		t.Fatalf("session stamp did not parse: %+v", sums[0])
	}
	var session CaptureSummary
	for _, s := range sums {
		if s.Name == "20260622T203110Z" {
			session = s
		}
	}
	if session.StallCount != 1 || session.WorstLagMS != 840 {
		t.Fatalf("session stall badge = count=%d worst=%d", session.StallCount, session.WorstLagMS)
	}
	row := testDisplay().CaptureRow(session)
	if !strings.Contains(row, "1 stalls") || !strings.Contains(row, "840ms") {
		t.Errorf("CaptureRow missing stall badge: %q", row)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
}
