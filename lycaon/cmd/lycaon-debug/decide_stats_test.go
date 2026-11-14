package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
)

// captureStdout runs run with the process stdout swapped for a pipe and
// returns what it wrote, for the subcommands that only print.
func captureStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	testutil.FailErr(t, "capture stdout", err)
	saved := os.Stdout
	os.Stdout = write
	runErr := run()
	os.Stdout = saved
	testutil.FailErr(t, "close captured stdout", write.Close())
	var out bytes.Buffer
	_, copyErr := io.Copy(&out, read)
	testutil.FailErr(t, "read captured stdout", copyErr)
	testutil.FailErr(t, "close captured stdout reader", read.Close())
	return out.String(), runErr
}

// statsReceipt writes one receipt with an explicit trigger and decisions body.
func (f exportFixture) statsReceipt(t *testing.T, sessionID, opening, trigger, decisions string) {
	t.Helper()
	_, err := store.NewSQL(f.handle).PutTurnLoadReceipt(t.Context(), store.TurnLoadReceipt{
		SessionID: sessionID, OpeningMessageID: opening, Trigger: trigger, SurfaceID: "implement_investigate",
		StateJSON: "{}", Decisions: decisions, Standing: "{}", CreatedAt: time.Now().UTC(),
	})
	testutil.FailErr(t, "put receipt", err)
}

func TestDecideStatsAggregatesLoadAndUseAcrossTurnReceipts(t *testing.T) {
	f := newExportFixture(t)
	// The first turn loaded jq_edit and web_search; the session called jq_edit.
	f.statsReceipt(t, f.root.ID, "u1", store.TurnLoadTriggerTurn,
		`{"turn":{"candidates":[{"id":"jq_edit"},{"id":"web_search"},{"id":"command"}],"tools":{"jq_edit":0.9,"web_search":0.8}}}`)
	// The second turn loaded only command and called nothing.
	f.statsReceipt(t, f.root.ID, "u2", store.TurnLoadTriggerTurn,
		`{"turn":{"candidates":[{"id":"jq_edit"},{"id":"command"}],"tools":{"command":0.7}}}`)
	// A request_tools resolution and an undecodable body never reach the counts.
	f.statsReceipt(t, f.root.ID, "u1", store.TurnLoadTriggerRequest, `{"turn":{"candidates":[{"id":"jq_edit"}]}}`)
	f.statsReceipt(t, f.root.ID, "u1", store.TurnLoadTriggerTurn, `{"turn":`)

	out, err := captureStdout(t, func() error { return runDecideStats([]string{"--db", f.path}) })
	testutil.FailErr(t, "decide stats", err)
	var report struct {
		Turns int         `json:"turns"`
		Tools []toolStats `json:"tools"`
	}
	testutil.FailErr(t, "decode stats", json.Unmarshal([]byte(out), &report))
	if report.Turns != 2 {
		t.Fatalf("turns = %d, want the two decodable turn receipts", report.Turns)
	}
	want := []toolStats{
		{ID: "command", Loaded: 1, Receipts: 2},
		{ID: "jq_edit", Loaded: 1, Used: 1, Receipts: 2},
		{ID: "web_search", Loaded: 1, Receipts: 1},
	}
	if len(report.Tools) != len(want) {
		t.Fatalf("tools = %+v, want %+v", report.Tools, want)
	}
	for i, row := range report.Tools {
		if row != want[i] {
			t.Errorf("tools[%d] = %+v, want %+v", i, row, want[i])
		}
	}

	// Receipts up to an id are skipped, so a rerun can resume where it stopped.
	out, err = captureStdout(t, func() error { return runDecideStats([]string{"--db", f.path, "--after", "2"}) })
	testutil.FailErr(t, "decide stats after", err)
	testutil.FailErr(t, "decode stats after", json.Unmarshal([]byte(out), &report))
	if report.Turns != 0 || len(report.Tools) != 0 {
		t.Fatalf("stats after the last counted receipt = %+v, want none", report)
	}
}

func TestDecideStatsRejectsBadInput(t *testing.T) {
	if err := runDecideStats(nil); err == nil || !strings.Contains(err.Error(), "--db is required") {
		t.Fatalf("missing --db error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "empty.db")
	testutil.FailErr(t, "write empty database", os.WriteFile(path, nil, 0o600))
	if err := runDecideStats([]string{"--db", path}); err == nil || !strings.Contains(err.Error(), "list receipts") {
		t.Fatalf("empty database error = %v", err)
	}
}
