package episode

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/trainingcapture"
	"github.com/lycaon/lycaon/pkg/api"
)

type bundleFixture struct {
	capture, captures string
	committed         string
}

// newBundleFixture is a closed episode: a root and child session, a settled
// read call, three settled outputs, capture records and one turn commit.
func newBundleFixture(t *testing.T) bundleFixture {
	t.Helper()
	capture := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(capture, "store.db"))
	project := testdbseed.DefaultProjectID
	testdbseed.InsertSession(t, database, "root", project)
	testdbseed.InsertSession(t, database, "child", project)
	_, err := database.ExecContext(t.Context(), `UPDATE sessions SET parent_session_id='root' WHERE id='child'`)
	testutil.FailErr(t, "bind child", err)
	history := store.NewSQL(database)
	history.SetDataDir(capture)
	turn, err := history.BeginTurn(t.Context(), store.TurnStart{SessionID: "root", ProjectID: project, Origin: store.TurnOriginUser, InputJSON: "{}"})
	testutil.FailErr(t, "begin turn", err)
	settled := map[string]string{}
	for i, output := range []store.ModelOutput{
		{ID: "o1", MessageID: "m1", ToolCallsJSON: `[{"id":"c1","name":"read","args":{"path":"a.go"}}]`},
		{ID: "o2", MessageID: "m2", Content: "Done."},
		{ID: "o3", MessageID: "m2", Content: "Done, revised."},
	} {
		output.TurnAttemptID, output.SessionID, output.Iteration, output.ProviderID, output.Model = turn.Attempt.ID, "root", i+1, "provider", "model"
		_, err := history.SettleModelOutput(t.Context(), output)
		testutil.FailErr(t, "settle output", err)
		settled[output.ID] = time.Date(2026, 11, 12, 10, i, 0, 0, time.UTC).Format(time.RFC3339Nano)
		_, err = database.ExecContext(t.Context(), `UPDATE model_outputs SET settled_at=? WHERE id=?`, settled[output.ID], output.ID)
		testutil.FailErr(t, "order settlement", err)
	}
	testutil.FailErr(t, "append messages", history.AppendMessages(t.Context(), "root",
		api.Message{ID: "m1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c1", Name: "read", Args: map[string]any{"path": "a.go"}}}},
		api.Message{ID: "m2", Role: api.MessageRoleAssistant, Content: "Done."}))
	read, _ := toolcontract.Lookup("read")
	recorder := invocation.NewSQLRecorder(database)
	receipt, err := recorder.Begin(t.Context(), invocation.Start{ProjectID: project, SessionID: "root", ToolCallID: "c1", ToolName: "read", Args: map[string]any{"path": "a.go"}, Contract: read})
	testutil.FailErr(t, "begin receipt", err)
	_, err = recorder.Settle(t.Context(), receipt.ID, invocation.Settlement{Status: api.InvocationStatusCompleted, Invoked: true, EvidenceKind: "result"})
	testutil.FailErr(t, "settle receipt", err)
	_, err = database.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	testutil.FailErr(t, "checkpoint capture", err)
	captures := filepath.Join(capture, "training-captures")
	testutil.FailErr(t, "capture dir", os.Mkdir(captures, 0o700))
	write := func(name string, value any) {
		raw, err := json.Marshal(value)
		testutil.FailErr(t, "encode capture", err)
		testutil.FailErr(t, "write capture", os.WriteFile(filepath.Join(captures, name), raw, 0o600))
	}
	surface := &trainingcapture.PromptSurface{Tools: []trainingcapture.OfferedTool{{Tool: "read", Source: "standing"}}, Units: []trainingcapture.RenderedUnit{{ID: "reading", AttachedBy: []string{"read"}}}}
	write("a.json", trainingcapture.Record{Schema: trainingcapture.Schema, SessionID: "root", MessageID: "m1", OutputID: "o1", CallID: "call-1",
		Dispatches: 1, Settled: true, PromptSurface: surface, Request: json.RawMessage(`{"messages":[]}`),
		StudentRequest: json.RawMessage(`{"messages":[],"tools":[]}`), Response: json.RawMessage(`{"content":"","tool_calls":[]}`)})
	write("b.json", trainingcapture.Record{Schema: trainingcapture.Schema, SessionID: "elsewhere", MessageID: "m9"})
	// The commit falls between o2 and o3, so o2 is the committed output.
	committed := time.Date(2026, 11, 12, 10, 1, 30, 0, time.UTC).Format(time.RFC3339Nano)
	write("c.json", trainingcapture.Fact{Schema: trainingcapture.FactSchema, Kind: "turn_committed", SessionID: "root", MessageID: "m2", DecidedAt: committed})
	return bundleFixture{capture: capture, captures: captures, committed: committed}
}

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(path)
	testutil.FailErr(t, "open lines", err)
	defer func() { _ = file.Close() }()
	var out []map[string]any
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var value map[string]any
		testutil.FailErr(t, "decode line", json.Unmarshal(scanner.Bytes(), &value))
		out = append(out, value)
	}
	return out
}

func digest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read bound file", err)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestABundleBindsEveryFileAndBlobAndNamesTheCommittedOutput(t *testing.T) {
	f := newBundleFixture(t)
	before := digest(t, filepath.Join(f.capture, "store.db"))
	out := filepath.Join(t.TempDir(), "bundle")
	manifest, err := WriteBundle(t.Context(), BundleRequest{Capture: f.capture, Captures: f.captures, SessionID: "root", Out: out})
	testutil.FailErr(t, "write bundle", err)
	if manifest.Schema != BundleSchema || manifest.RootSessionID != "root" || manifest.StoreSHA256 != before {
		t.Fatalf("manifest = %+v", manifest)
	}
	if digest(t, filepath.Join(f.capture, "store.db")) != before {
		t.Fatal("export changed the capture")
	}
	for _, name := range BundleFiles() {
		if manifest.Files[name] != digest(t, filepath.Join(out, name)) {
			t.Fatalf("%s is not bound by the manifest", name)
		}
	}
	for _, blob := range manifest.Blobs {
		if digest(t, filepath.Join(out, "blobs", blob)) != blob {
			t.Fatalf("blob %s does not hash to its name", blob)
		}
	}
	sessions := readLines(t, filepath.Join(out, "sessions.jsonl"))
	if len(sessions) != 2 || sessions[1]["parent_id"] != "root" {
		t.Fatalf("sessions = %+v", sessions)
	}
	var messages []api.Message
	raw, err := os.ReadFile(filepath.Join(out, "blobs", sessions[0]["messages_blob"].(string)))
	testutil.FailErr(t, "read transcript blob", err)
	testutil.FailErr(t, "decode transcript", json.Unmarshal(raw, &messages))
	if len(messages) != 2 || messages[0].ToolCalls[0].ID != "c1" {
		t.Fatalf("transcript = %+v", messages)
	}
	captures := readLines(t, filepath.Join(out, "captures.jsonl"))
	if len(captures) != 1 || captures[0]["request"] != nil || captures[0]["student_request_blob"] == nil || captures[0]["prompt_surface"] == nil {
		t.Fatalf("captures = %+v", captures)
	}
	decisions := readLines(t, filepath.Join(out, "decisions.jsonl"))
	if len(decisions) != 1 || decisions[0]["output_id"] != "o2" || decisions[0]["decided_at"] != f.committed {
		t.Fatalf("decisions = %+v", decisions)
	}
	operations := readLines(t, filepath.Join(out, "operations.jsonl"))
	if len(operations) != 1 || operations[0]["message_id"] != "m1" || operations[0]["semantic_outcome"] != "success" || operations[0]["effect_state"] != "known" {
		t.Fatalf("operations = %+v", operations)
	}
	completions := readLines(t, filepath.Join(out, "completions.jsonl"))
	if len(completions) != 3 {
		t.Fatalf("completions = %+v", completions)
	}
	raw, err = os.ReadFile(filepath.Join(out, "blobs", completions[0]["body_blob"].(string)))
	testutil.FailErr(t, "read output body", err)
	if !strings.Contains(string(raw), `"a.go"`) {
		t.Fatalf("output body = %s", raw)
	}
	var catalog TrainingContract
	raw, err = os.ReadFile(filepath.Join(out, "catalog.json"))
	testutil.FailErr(t, "read catalog", err)
	testutil.FailErr(t, "decode catalog", json.Unmarshal(raw, &catalog))
	if catalog.Bundle != BundleSchema || len(catalog.Sources) != 2 {
		t.Fatalf("catalog = %+v", catalog)
	}
	if _, err := WriteBundle(t.Context(), BundleRequest{Capture: f.capture, Captures: f.captures, SessionID: "root", Out: out}); err == nil {
		t.Fatal("an existing bundle was overwritten")
	}
}

func TestABundleRefusesUnknownCapturesAndIncompleteRequests(t *testing.T) {
	f := newBundleFixture(t)
	testutil.FailErr(t, "write foreign record", os.WriteFile(filepath.Join(f.captures, "d.json"), []byte(`{"schema":"other/1","session_id":"root"}`), 0o600))
	if _, err := WriteBundle(t.Context(), BundleRequest{Capture: f.capture, Captures: f.captures, SessionID: "root", Out: filepath.Join(t.TempDir(), "b")}); err == nil || !strings.Contains(err.Error(), "unknown schema") {
		t.Fatalf("unknown schema: %v", err)
	}
	if _, err := WriteBundle(t.Context(), BundleRequest{Capture: f.capture, SessionID: "root", Out: filepath.Join(t.TempDir(), "b")}); err == nil {
		t.Fatal("a bundle without its training captures was written")
	}
	out := filepath.Join(t.TempDir(), "b")
	manifest, err := WriteBundle(t.Context(), BundleRequest{Capture: f.capture, Captures: filepath.Join(t.TempDir(), "none"), SessionID: "root", Out: out})
	testutil.FailErr(t, "bundle without captures", err)
	if manifest.Files["captures.jsonl"] != digest(t, filepath.Join(out, "captures.jsonl")) || len(readLines(t, filepath.Join(out, "captures.jsonl"))) != 0 {
		t.Fatalf("empty captures = %+v", manifest)
	}
}

func TestACommitBeforeAnySettledOutputNamesNone(t *testing.T) {
	fact := trainingcapture.Fact{MessageID: "m2", DecidedAt: "2026-11-12T09:00:00Z"}
	outputs := []store.ModelOutput{{ID: "o2", MessageID: "m2", SettledAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}}
	if got := committedOutput(outputs, fact); got != "" {
		t.Fatalf("committed output = %q", got)
	}
	if got := committedOutput(outputs, trainingcapture.Fact{MessageID: "m2", DecidedAt: "not a time"}); got != "" {
		t.Fatalf("unparseable commit = %q", got)
	}
}
