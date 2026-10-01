package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// exportFixture is a store with one coordinator session, one worker leg, and
// the receipts their turns wrote.
type exportFixture struct {
	handle  *db.Store
	path    string
	root    *api.Session
	worker  *api.Session
	started time.Time
}

func newExportFixture(t *testing.T) exportFixture {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "store.db")
	handle := testdbfixture.OpenPath(t, path)
	testdbseed.InsertProjectRoot(t, handle, testdbseed.DefaultProjectID, t.TempDir())
	_, err := handle.ExecContext(ctx, `UPDATE projects SET name = 'flask' WHERE id = ?`, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "name project", err)
	st := store.NewSQL(handle)
	root, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	f := exportFixture{handle: handle, path: path, root: root, started: time.Now().UTC().Add(-time.Minute)}
	testutil.FailErr(t, "append coordinator turns", st.AppendMessages(ctx, root.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "rewrite the version field in package.json"},
		api.Message{ID: "a1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{ID: "c1", Name: "read", Args: map[string]any{"path": "package.json"}},
			{ID: "c2", Name: "request_tools", Args: map[string]any{"need": "edit a JSON field with jq_edit", "requests": []any{"#process", "web_search"}}},
		}},
		api.Message{ID: "a2", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{ID: "c3", Name: "jq_edit", Args: map[string]any{}},
			{ID: "c4", Name: "skills_read", Args: map[string]any{"name": "release-notes"}},
		}},
		api.Message{ID: "u2", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "thanks"},
		api.Message{ID: "a3", Role: api.MessageRoleAssistant, Content: "You're welcome."},
	))
	f.worker, err = st.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: "implement", Prompt: "search the web for the changelog format"})
	testutil.FailErr(t, "create worker", err)
	testutil.FailErr(t, "append worker leg", st.AppendMessages(ctx, f.worker.ID,
		api.Message{ID: "w1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c5", Name: "web_search", Args: map[string]any{"query": "keep a changelog"}}}},
	))
	f.turn(t, root.ID, "complete")
	f.turn(t, f.worker.ID, "complete")
	f.llmCall(t, root.ID, "qwen3.8-27b")
	f.llmCall(t, f.worker.ID, "gemma-4-31b-it")
	return f
}

func (f exportFixture) turn(t *testing.T, sessionID, status string) {
	t.Helper()
	at := f.started.Format(time.RFC3339Nano)
	_, err := f.handle.ExecContext(t.Context(), `INSERT INTO turns (id, session_id, project_id, origin, input_json, status, created_at, updated_at, progressed_at)
		VALUES (?, ?, ?, 'user', '', ?, ?, ?, ?)`, "turn-"+sessionID, sessionID, testdbseed.DefaultProjectID, status, at, at, at)
	testutil.FailErr(t, "insert turn", err)
}

func (f exportFixture) llmCall(t *testing.T, sessionID, model string) {
	t.Helper()
	_, err := f.handle.ExecContext(t.Context(), `INSERT INTO llm_calls (id, session_id, model, caller, status, started_at) VALUES (?, ?, ?, 'coordinator', 'reported', ?)`,
		"call-"+sessionID, sessionID, model, f.started.Format(time.RFC3339Nano))
	testutil.FailErr(t, "insert llm call", err)
}

func (f exportFixture) receipt(t *testing.T, sessionID, opening, state, decisions string) {
	t.Helper()
	_, err := store.NewSQL(f.handle).PutTurnLoadReceipt(t.Context(), store.TurnLoadReceipt{
		SessionID: sessionID, OpeningMessageID: opening, Trigger: store.TurnLoadTriggerTurn, SurfaceID: "implement_investigate",
		StateJSON: state, Decisions: decisions, Standing: "{}", Abstained: true, Reason: "engine disabled", CreatedAt: time.Now().UTC(),
	})
	testutil.FailErr(t, "put receipt", err)
}

// rowSchemaValidator compiles scripts/bialy/row.schema.json, the shape the
// offline trainer reads, so the exporter cannot drift from it.
func rowSchemaValidator(t *testing.T) *jsonschema.Schema {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "bialy", "row.schema.json"))
	testutil.FailErr(t, "resolve row schema", err)
	f, err := os.Open(path) // #nosec G304 -- repository schema
	testutil.FailErr(t, "open row schema", err)
	defer func() { _ = f.Close() }()
	doc, err := jsonschema.UnmarshalJSON(f)
	testutil.FailErr(t, "decode row schema", err)
	compiler := jsonschema.NewCompiler()
	testutil.FailErr(t, "add row schema", compiler.AddResource(path, doc))
	schema, err := compiler.Compile(path)
	testutil.FailErr(t, "compile row schema", err)
	return schema
}

func exportRows(t *testing.T, dbPath string, extra ...string) []decideRow {
	t.Helper()
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	testutil.FailErr(t, "export", runDecideExport(append([]string{"--db", dbPath, "--out", out}, extra...)))
	raw, err := os.ReadFile(out) // #nosec G304 -- test temp file
	testutil.FailErr(t, "read rows", err)
	schema := rowSchemaValidator(t)
	var rows []decideRow
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(line))
		testutil.FailErr(t, "decode row for schema", err)
		testutil.FailErr(t, "row matches row.schema.json", schema.Validate(instance))
		var row decideRow
		testutil.FailErr(t, "decode row", json.Unmarshal(line, &row))
		rows = append(rows, row)
	}
	return rows
}

func TestDecideExportWritesCoordinatorAndWorkerRows(t *testing.T) {
	f := newExportFixture(t)
	candidates := `"floor":["read","request_tools"],"candidates":{"loadable":["jq_edit","web_search","command"],"guides":[]}`
	f.receipt(t, f.root.ID, "u1", `{"host":"coordinator","user":"rewrite the version field in package.json","surface":"implement_investigate"}`, "{"+candidates+"}")
	f.receipt(t, f.root.ID, "u2", `{"host":"coordinator","user":"thanks"}`, `{"floor":["read"]}`)
	msgs, err := db.New(f.handle).ListSessionMessages(t.Context(), f.worker.ID)
	testutil.FailErr(t, "list worker messages", err)
	workerOpening := msgs[0].ID
	f.receipt(t, f.worker.ID, workerOpening, `{"host":"worker","user":"search the web for the changelog format","surface":"implement"}`, "{"+candidates+"}")

	rows := exportRows(t, f.path)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the coordinator and worker turns (the receipt without candidates is skipped): %+v", len(rows), rows)
	}
	coord, worker := rows[0], rows[1]
	if coord.Schema != rowSchema || coord.Host != "coordinator" || coord.Project != "flask" || coord.Model != "qwen3.8-27b" || coord.Partial {
		t.Fatalf("coordinator row = %+v", coord)
	}
	if !slices.Equal(coord.Labels.Tools, []string{"jq_edit"}) || coord.Labels.Kind == nil || *coord.Labels.Kind != "change" {
		t.Fatalf("coordinator labels = %+v, want loadable tools [jq_edit] and kind change", coord.Labels)
	}
	if len(coord.Labels.Requests) != 1 || !slices.Equal(coord.Labels.Requests[0].Exact, []string{"jq_edit"}) || !slices.Equal(coord.Labels.Requests[0].After, []string{"jq_edit"}) {
		t.Fatalf("requests = %+v", coord.Labels.Requests)
	}
	if !slices.Equal(coord.Labels.RequestedGroups, []string{"#process"}) || !slices.Equal(coord.Labels.RequestedNames, []string{"web_search"}) || !slices.Equal(coord.Labels.Skills, []string{"release-notes"}) {
		t.Fatalf("requested = %+v", coord.Labels)
	}
	if coord.Engine.State != "abstained" || !slices.Equal(coord.Offered.Loadable, []string{"jq_edit", "web_search", "command"}) {
		t.Fatalf("engine/offered = %+v / %+v", coord.Engine, coord.Offered)
	}
	if worker.Host != "worker" || worker.RootSession != f.root.ID || worker.Model != "gemma-4-31b-it" || !slices.Equal(worker.Labels.Tools, []string{"web_search"}) {
		t.Fatalf("worker row = %+v", worker)
	}

	roots := filepath.Join(t.TempDir(), "roots.txt")
	testutil.FailErr(t, "write roots", os.WriteFile(roots, []byte("someone-else\n"), 0o600))
	if got := exportRows(t, f.path, "--roots", roots); len(got) != 0 {
		t.Fatalf("--roots kept %d rows from sessions outside it", len(got))
	}
}

func TestDecideExportKeepsOnlyTheNeedsOfAnIncompleteTurn(t *testing.T) {
	f := newExportFixture(t)
	_, err := f.handle.ExecContext(t.Context(), `UPDATE turns SET status = 'failed' WHERE session_id = ?`, f.root.ID)
	testutil.FailErr(t, "fail turn", err)
	f.receipt(t, f.root.ID, "u1", `{"host":"coordinator","user":"rewrite"}`, `{"floor":[],"candidates":{"loadable":["jq_edit"],"guides":[]}}`)
	rows := exportRows(t, f.path)
	if len(rows) != 1 || !rows[0].Partial || len(rows[0].Labels.Tools) != 0 || len(rows[0].Labels.Requests) != 1 || rows[0].Labels.Kind != nil {
		t.Fatalf("rows = %+v, want one partial row carrying only its request", rows)
	}
}

func TestDecideExportDistinguishesMissingCandidatesFromCorruptDecisions(t *testing.T) {
	exporter := rowExporter{}
	err := exporter.export(t.Context(), db.ListTurnReceiptContextsRow{ID: 42, DecisionsJson: `{"candidates":`})
	var syntaxError *json.SyntaxError
	if !errors.As(err, &syntaxError) || exporter.noCandidates != 0 {
		t.Fatalf("corrupt decisions: error=%v skipped=%d", err, exporter.noCandidates)
	}
	err = exporter.export(t.Context(), db.ListTurnReceiptContextsRow{ID: 43, DecisionsJson: `{}`})
	testutil.FailErr(t, "export receipt without candidates", err)
	if exporter.noCandidates != 1 {
		t.Fatalf("receipts without candidates=%d, want 1", exporter.noCandidates)
	}
}
