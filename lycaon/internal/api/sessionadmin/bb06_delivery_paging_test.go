package sessionadmin_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/actioncard"
	"github.com/lycaon/lycaon/internal/activitybriefing"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/api/briefingadmin"
	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefinghost"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type bb06Switchable struct{ enabled bool }

func (s *bb06Switchable) Enabled() bool                 { return s.enabled }
func (s *bb06Switchable) PutEnabled(enabled bool) error { s.enabled = enabled; return nil }

type bb06Generator struct{}

func (bb06Generator) Generate(context.Context, briefing.Invocation) (string, string, string, error) {
	return "Summarized activity.", "provider", "model", nil
}

type bb06CausalReader struct {
	calls map[string]briefing.CausalRead
}

func (r bb06CausalReader) ReadActionCause(_ context.Context, ref briefing.ActionRef) (briefing.CausalRead, error) {
	if got, ok := r.calls[ref.ToolCallID]; ok {
		return got, nil
	}
	return briefing.CausalRead{}, nil
}

type bb06Fixture struct {
	server   *api.Server
	db       db.Handle
	recorder *invocation.SQLRecorder
	project  *project.Project
	session  *wire.Session
	causal   bb06CausalReader
}

func newBB06Fixture(t *testing.T) *bb06Fixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "bb06-delivery.db")
	reg := project.NewSQLRegistry(sqlDB)
	p, err := projecttest.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "create project", err)

	sessStore := store.NewSQL(sqlDB)
	sess, err := sessStore.Sessions.Create(t.Context(), wire.CreateSessionRequest{
		Posture:   wire.SessionPostureBuild,
		ProjectID: p.ID,
	}, p.ID)
	testutil.FailErr(t, "create session", err)

	recorder := invocation.NewSQLRecorder(sqlDB)
	ledger := sourceledger.New(sqlDB, t.TempDir(), sourceledger.Timing{})

	causal := bb06CausalReader{calls: map[string]briefing.CausalRead{}}
	host := briefinghost.New(context.Background(), sqlDB, t.TempDir(), bb06Generator{})
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	testutil.FailErr(t, "register tool_activity briefing profile", host.Registry.Register(
		activitybriefing.New(sessStore.Sessions, recorder, causal),
	))
	files := filebriefing.NewService(context.Background(), filebriefing.Dependencies{
		Store:    filebriefing.NewSQL(sqlDB),
		Settings: &bb06Switchable{enabled: true},
	})
	t.Cleanup(func() { files.Stop(); files.Wait(context.Background()) })

	deps := apitest.Dependencies(t, api.Dependencies{
		Core: api.CoreDependencies{
			Database:    sqlDB,
			Store:       sessStore,
			Projects:    reg,
			Invocations: recorder,
			Sessions:    session.NewHost(sessStore, session.Models{Limits: settings.DefaultSessionLimits()}, nil),
		},
		Source: api.SourceDependencies{
			SourceLedger: ledger,
			Briefings:    briefingadmin.Services{Files: files, Host: host},
		},
	})
	srv := api.NewServer(deps, nil, api.TestAPIToken)
	return &bb06Fixture{
		server:   srv,
		db:       sqlDB,
		recorder: recorder,
		project:  p,
		session:  sess,
		causal:   causal,
	}
}

func (f *bb06Fixture) get(target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, contractfixture.NewAuthedRequest(http.MethodGet, target, nil))
	return w
}

func (f *bb06Fixture) postJSON(target string, payload any) *httptest.ResponseRecorder {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, contractfixture.NewAuthedRequest(http.MethodPost, target, strings.NewReader(string(raw))))
	return w
}

type seededProgram struct {
	programID string
	parentID  string
	rootID    string
	callID    string
	defDigest string
}

func (f *bb06Fixture) beginProgram(t *testing.T, callID, cmdText string, nodeIDs ...string) seededProgram {
	t.Helper()
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract missing")
	}
	parent, err := f.recorder.Begin(t.Context(), invocation.Start{
		ProjectID:  f.project.ID,
		SessionID:  f.session.ID,
		ToolCallID: callID,
		ToolName:   "command",
		Args:       map[string]any{"command": cmdText},
		Contract:   contract,
	})
	testutil.FailErr(t, "begin invocation", err)

	programID := uuid.NewString()
	rootID := uuid.NewString()
	digest := sha256.Sum256([]byte(cmdText))
	sourceDigest := hex.EncodeToString(digest[:])
	span := toolcontract.SourceSpan{StartByte: 0, EndByte: len(cmdText), Line: 1, Column: 1}
	nodes := make([]toolcontract.ProgramNode, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		nodes = append(nodes, toolcontract.ProgramNode{ID: id, SourceUnitID: "submitted", Span: span})
	}
	start := toolcontract.ProgramStart{
		Version: toolcontract.ProgramVersion{
			ProgramID:       programID,
			ParentReceiptID: parent.ID,
			RootExecutionID: rootID,
			LanguageVersion: toolcontract.LanguageVersion,
			CatalogDigest:   toolcontract.LanguageCatalogDigest(),
			SourceUnitID:    "submitted",
			SourceDigest:    sourceDigest,
			SourceReference: "output:exact-program",
		},
		PlanReference:   "output:validated-plan",
		SourceExact:     true,
		SyntaxValidated: true,
		Sources: []toolcontract.ProgramSource{{
			UnitID:          "submitted",
			Pin:             toolcontract.SourcePin{SourceID: "submitted-program", Revision: sourceDigest, Representation: toolcontract.UTF8SourceBytes, SHA256: sourceDigest, EndByte: int64(len(cmdText))},
			DetailReference: "output:exact-program",
		}},
		Nodes: nodes,
	}
	testutil.FailErr(t, "begin program", f.recorder.BeginProgram(t.Context(), start))

	defJSON := `{"Commands":[{"name":"head","family":"inspect"},{"name":"grep","family":"search"},{"name":"external","family":"process"}]}`
	defSum := sha256.Sum256([]byte(defJSON))
	defDigest := hex.EncodeToString(defSum[:])
	_, err = f.db.ExecContext(t.Context(),
		`INSERT INTO invocation_program_definitions (program_id, definition_digest, operation_id, owner, lifecycle, definition_json) VALUES (?, ?, 'command', 'program', 'read_only', ?)`,
		programID, defDigest, defJSON)
	testutil.FailErr(t, "insert program definition", err)

	return seededProgram{
		programID: programID,
		parentID:  parent.ID,
		rootID:    rootID,
		callID:    callID,
		defDigest: defDigest,
	}
}

type seededExec struct {
	id            string
	nodeID        string
	ordinal       int
	parentExecID  string
	operationID   string
	state         string
	owner         string
	lifecycle     string
	nativeCommand string
	effectMode    string
	effectClasses string
	canonical     string
	statusCode    *int
	ownerStarted  int
}

func (f *bb06Fixture) insertExecution(t *testing.T, p seededProgram, e seededExec) {
	t.Helper()
	var status sql.NullInt64
	if e.statusCode != nil {
		status = sql.NullInt64{Int64: int64(*e.statusCode), Valid: true}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO invocation_program_executions (
		id, program_id, node_id, ordinal, parent_execution_id, actor_id, scope_json,
		operation_id, definition_digest, arguments_representation, arguments_digest, arguments_ref,
		canonical, state, owner, lifecycle, native_command, effect_mode, effect_classes_json,
		started_at, status_code, effect_certainty, owner_started, complete, publication_eligible
	) VALUES (?, ?, ?, ?, ?, 'fixture-person', '{"kind":"session"}', ?, ?, 'operation_arguments_v1', '', '', ?, ?, ?, ?, ?, ?, ?, ?, ?, 'none', ?, 1, 1)`,
		e.id, p.programID, e.nodeID, e.ordinal, e.parentExecID,
		e.operationID, p.defDigest, e.canonical, e.state, e.owner, e.lifecycle,
		e.nativeCommand, e.effectMode, e.effectClasses, now, status, e.ownerStarted,
	)
	testutil.FailErr(t, "insert execution "+e.id, err)
}

// TestBB06_M10_ProgramReceiptPagingAndRetention verifies M10 backend paging,
// small page sizes, scoped paging, cursor propagation, invalid cursor errors,
// and unavailable program reference handling (no synthetic success).
func TestBB06_M10_ProgramReceiptPagingAndRetention(t *testing.T) {
	f := newBB06Fixture(t)
	p := f.beginProgram(t, "call-m10-paging", "grep pattern *.go", "n1", "n2", "n3")
	zero := 0
	execIDs := make([]string, 5)
	for i := range execIDs {
		execIDs[i] = uuid.NewString()
		f.insertExecution(t, p, seededExec{
			id:            execIDs[i],
			nodeID:        "n1",
			ordinal:       i + 1,
			parentExecID:  p.rootID,
			operationID:   "grep",
			state:         "completed",
			owner:         "filesystem",
			lifecycle:     "read_only",
			nativeCommand: "grep",
			effectMode:    "read_only",
			effectClasses: `["read_only"]`,
			canonical:     fmt.Sprintf("grep pattern file-%d.go", i+1),
			statusCode:    &zero,
			ownerStarted:  1,
		})
	}
	testutil.FailErr(t, "settle program", f.recorder.SettleProgram(t.Context(), p.programID, toolcontract.ProgramSettlement{
		State: "completed", Status: &zero, DetailReference: "output:done",
	}))

	activityID := execIDs[0]
	for _, limit := range []int{1, 2} {
		for _, scoped := range []bool{false, true} {
			name := fmt.Sprintf("limit=%d/scoped=%v", limit, scoped)
			t.Run(name, func(t *testing.T) {
				var collected []string
				seen := map[string]bool{}
				cursor := ""
				pages := 0
				for {
					q := url.Values{"limit": {fmt.Sprint(limit)}}
					if scoped {
						q.Set("activity_id", activityID)
					}
					if cursor != "" {
						q.Set("cursor", cursor)
					}
					resp := f.get("/v1/sessions/" + f.session.ID + "/program-receipts/" + p.programID + "?" + q.Encode())
					if resp.Code != http.StatusOK {
						t.Fatalf("page %d: status=%d body=%s", pages, resp.Code, resp.Body.String())
					}
					var page wire.CommandProgramReceiptPage
					testutil.FailErr(t, "decode page", json.Unmarshal(resp.Body.Bytes(), &page))
					pages++
					if len(page.Activities) != 1 || page.Activities[0].ID != activityID {
						t.Fatalf("page %d activity identity changed: %+v", pages, page.Activities)
					}
					for _, ex := range page.Executions {
						if seen[ex.ID] {
							t.Fatalf("duplicate execution %s on page %d", ex.ID, pages)
						}
						seen[ex.ID] = true
						collected = append(collected, ex.ID)
					}
					if page.NextCursor == "" {
						if page.ExecutionsOmitted != 0 || page.NodesOmitted != 0 {
							t.Fatalf("final page omitted items without cursor: exec=%d nodes=%d", page.ExecutionsOmitted, page.NodesOmitted)
						}
						break
					}
					cursor = page.NextCursor
					if pages > 10 {
						t.Fatal("paging loop did not terminate")
					}
				}
				if pages <= 2 {
					t.Fatalf("expected >2 receipt pages for limit=%d, got %d", limit, pages)
				}
				if len(collected) != len(execIDs) {
					t.Fatalf("collected %d executions, want %d", len(collected), len(execIDs))
				}
				for i, want := range execIDs {
					if collected[i] != want {
						t.Fatalf("execution[%d] = %s, want %s", i, collected[i], want)
					}
				}
			})
		}
	}

	t.Run("invalid_and_cross_scope_cursor", func(t *testing.T) {
		bad := f.get("/v1/sessions/" + f.session.ID + "/program-receipts/" + p.programID + "?limit=2&cursor=not-a-valid-cursor")
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("invalid cursor status = %d, want 400: %s", bad.Code, bad.Body.String())
		}
		var errResp wire.ErrorResponse
		testutil.FailErr(t, "decode cursor error", json.Unmarshal(bad.Body.Bytes(), &errResp))
		if errResp.Code != wire.ApiErrorCodeInvalidPageCursor {
			t.Fatalf("invalid cursor code = %q, want %q", errResp.Code, wire.ApiErrorCodeInvalidPageCursor)
		}

		first := f.get("/v1/sessions/" + f.session.ID + "/program-receipts/" + p.programID + "?limit=1&activity_id=" + activityID)
		if first.Code != http.StatusOK {
			t.Fatalf("first scoped page status = %d: %s", first.Code, first.Body.String())
		}
		var page wire.CommandProgramReceiptPage
		testutil.FailErr(t, "decode first scoped page", json.Unmarshal(first.Body.Bytes(), &page))
		cross := f.get("/v1/sessions/" + f.session.ID + "/program-receipts/" + p.programID + "?limit=1&cursor=" + url.QueryEscape(page.NextCursor))
		if cross.Code != http.StatusBadRequest {
			t.Fatalf("cross-scope cursor status = %d, want 400: %s", cross.Code, cross.Body.String())
		}
	})

	t.Run("unavailable_program_reference_no_synthetic_success", func(t *testing.T) {
		missing := uuid.NewString()
		for _, path := range []string{
			"/v1/sessions/" + f.session.ID + "/program-receipts/" + missing + "?limit=6",
			"/v1/sessions/" + f.session.ID + "/program-receipt-document?scopes=" + missing + "&offset=0&limit=64",
		} {
			resp := f.get(path)
			if resp.Code != http.StatusNotFound {
				t.Fatalf("GET %s status = %d, want 404: %s", path, resp.Code, resp.Body.String())
			}
			var errResp wire.ErrorResponse
			testutil.FailErr(t, "decode unavailable error", json.Unmarshal(resp.Body.Bytes(), &errResp))
			if errResp.Code != wire.ApiErrorCodeCommandProgramNotFound {
				t.Fatalf("GET %s code = %q, want %q", path, errResp.Code, wire.ApiErrorCodeCommandProgramNotFound)
			}
		}
	})
}

// TestBB06_L02_PipelineReadSubExecutionReceiptScope reproduces historical lead
// L02: a 3-stage pipeline (program | -> read [owner=filesystem, read_only] ->
// command) emits preview.receipts {program_id, activity_id=read_exec_id} while
// read is active/fallback, then folds read into command's activity. Fetching
// /program-receipts and /program-receipt-document with the emitted read_exec_id
// post-settlement must not 404 command_program_not_found.
func TestBB06_L02_PipelineReadSubExecutionReceiptScope(t *testing.T) {
	f := newBB06Fixture(t)
	p := f.beginProgram(t, "call-l02-pipe", "head | external-tool", "pipe-node", "read-node", "cmd-node")
	pipeID := uuid.NewString()
	readID := uuid.NewString()
	cmdID := uuid.NewString()

	// Stage 1: pipeline container.
	f.insertExecution(t, p, seededExec{
		id: pipeID, nodeID: "pipe-node", ordinal: 1, parentExecID: p.rootID,
		operationID: "program |", state: "running", owner: "program",
		lifecycle: "read_only", nativeCommand: "program |", effectMode: "read_only",
		effectClasses: `["read_only"]`, canonical: "head | external-tool", ownerStarted: 1,
	})
	// Stage 2: read sub-execution (owner=filesystem, lifecycle=read_only) enters
	// a waiting/fallback state before stage 3 arrives, so live SSE emits it.
	f.insertExecution(t, p, seededExec{
		id: readID, nodeID: "read-node", ordinal: 2, parentExecID: pipeID,
		operationID: "read", state: "waiting", owner: "filesystem",
		lifecycle: "read_only", nativeCommand: "head", effectMode: "read_only",
		effectClasses: `["read_only"]`, canonical: "head", ownerStarted: 1,
	})

	liveSummary, err := f.recorder.LiveActivitySummary(t.Context(), f.session.ID, p.programID)
	testutil.FailErr(t, "read live activity summary", err)
	if len(liveSummary.Activities) != 1 || liveSummary.Activities[0].ID != readID {
		t.Fatalf("expected live fallback activity for read %s, got %+v", readID, liveSummary.Activities)
	}
	descriptor := actioncard.ForActivity(liveSummary, liveSummary.Activities[0], wire.ToolResult{
		Tool: "command", ToolCallID: p.callID,
	}, nil)
	if descriptor.Preview == nil || len(descriptor.Preview.Receipts) != 1 ||
		descriptor.Preview.Receipts[0].ProgramID != p.programID ||
		descriptor.Preview.Receipts[0].ActivityID != readID {
		t.Fatalf("unexpected SSE preview.receipts: %+v", descriptor.Preview)
	}
	emitted := descriptor.Preview.Receipts[0]

	// Live reads with the emitted {program_id, activity_id=readID} succeed.
	liveReceipt := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipts/%s?limit=6&activity_id=%s", f.session.ID, emitted.ProgramID, emitted.ActivityID))
	if liveReceipt.Code != http.StatusOK {
		t.Fatalf("live /program-receipts status = %d, want 200: %s", liveReceipt.Code, liveReceipt.Body.String())
	}
	liveDoc := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipt-document?scopes=%s:%s&offset=0&limit=64", f.session.ID, emitted.ProgramID, emitted.ActivityID))
	if liveDoc.Code != http.StatusOK {
		t.Fatalf("live /program-receipt-document status = %d, want 200: %s", liveDoc.Code, liveDoc.Body.String())
	}

	// Stage 3 arrives (command, owner=processes) and the pipeline settles.
	zero := 0
	_, err = f.db.ExecContext(t.Context(), `UPDATE invocation_program_executions SET state='completed', status_code=0 WHERE id IN (?, ?)`, pipeID, readID)
	testutil.FailErr(t, "complete pipe and read", err)
	f.insertExecution(t, p, seededExec{
		id: cmdID, nodeID: "cmd-node", ordinal: 3, parentExecID: pipeID,
		operationID: "command", state: "completed", owner: "processes",
		lifecycle: "effect_attempt", nativeCommand: "external", effectMode: "external",
		effectClasses: `["external"]`, canonical: "external-tool", statusCode: &zero, ownerStarted: 1,
	})
	testutil.FailErr(t, "settle pipeline program", f.recorder.SettleProgram(t.Context(), p.programID, toolcontract.ProgramSettlement{
		State: "completed", Status: &zero, DetailReference: "output:pipe-done",
	}))

	// Post-settlement reads using the SSE-emitted {program_id, readID}.
	settledReceipt := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipts/%s?limit=6&activity_id=%s", f.session.ID, emitted.ProgramID, emitted.ActivityID))
	if settledReceipt.Code != http.StatusOK {
		t.Errorf("DEFECT L02 REPRODUCED: post-settlement GET /program-receipts/%s?limit=6&activity_id=%s returned %d (want 200): %s",
			emitted.ProgramID, emitted.ActivityID, settledReceipt.Code, settledReceipt.Body.String())
	}
	settledDoc := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipt-document?scopes=%s:%s&offset=0&limit=64", f.session.ID, emitted.ProgramID, emitted.ActivityID))
	if settledDoc.Code != http.StatusOK {
		t.Errorf("DEFECT L02 REPRODUCED: post-settlement GET /program-receipt-document?scopes=%s:%s returned %d (want 200): %s",
			emitted.ProgramID, emitted.ActivityID, settledDoc.Code, settledDoc.Body.String())
	}
}

// TestBB06_L06_BriefingsMissingOrFoldedProgramReceipt reproduces historical
// lead L06: POST /v1/projects/{pid}/briefings with a missing or folded program
// receipt must return a typed 4xx refusal (not 500 internal_error).
func TestBB06_L06_BriefingsMissingOrFoldedProgramReceipt(t *testing.T) {
	f := newBB06Fixture(t)
	p := f.beginProgram(t, "call-l06", "grep needle main.go", "grep-node")
	execID := uuid.NewString()
	zero := 0
	f.insertExecution(t, p, seededExec{
		id: execID, nodeID: "grep-node", ordinal: 1, parentExecID: p.rootID,
		operationID: "grep", state: "completed", owner: "filesystem",
		lifecycle: "read_only", nativeCommand: "grep", effectMode: "read_only",
		effectClasses: `["read_only"]`, canonical: "grep needle main.go",
		statusCode: &zero, ownerStarted: 1,
	})
	testutil.FailErr(t, "settle program", f.recorder.SettleProgram(t.Context(), p.programID, toolcontract.ProgramSettlement{
		State: "completed", Status: &zero, DetailReference: "output:done",
	}))
	f.causal.calls[p.callID] = briefing.CausalRead{
		Found: true,
		Call:  briefing.CallExcerpt{Name: "command", Args: `{"command":"grep needle main.go"}`},
	}

	reqBody := func(programID, activityID string) map[string]any {
		return map[string]any{
			"profile_id": activitybriefing.ProfileID,
			"trigger":    "manual",
			"target_ref": wire.ToolActivityBriefingTarget{
				SessionID:   f.session.ID,
				ToolCallIds: []string{p.callID},
				Members:     []wire.ToolActivityBriefingMember{{ProgramID: programID, ActivityID: activityID}},
			},
		}
	}

	t.Run("valid_receipt", func(t *testing.T) {
		resp := f.postJSON("/v1/projects/"+f.project.ID+"/briefings", reqBody(p.programID, execID))
		if resp.Code != http.StatusOK {
			t.Fatalf("valid briefing status = %d, want 200: %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("missing_receipt_must_not_500", func(t *testing.T) {
		missingProgram := uuid.NewString()
		missingActivity := uuid.NewString()
		resp := f.postJSON("/v1/projects/"+f.project.ID+"/briefings", reqBody(missingProgram, missingActivity))
		if resp.Code == http.StatusInternalServerError {
			t.Errorf("DEFECT L06 REPRODUCED: POST /v1/projects/%s/briefings with missing receipt returned 500 internal_error instead of typed 404/409: %s",
				f.project.ID, resp.Body.String())
		}
	})
}

// TestBB06_L07_SourceCommandWindowsReadOnlyVsMutating verifies backend command
// window retention: read-only windows without operations or coverage gaps are
// intentionally pruned (returning 404 source_history_not_found), while mutating
// or gapped windows remain readable (200 OK).
func TestBB06_L07_SourceCommandWindowsReadOnlyVsMutating(t *testing.T) {
	f := newBB06Fixture(t)
	q := db.New(f.db)

	readOnlyWin := uuid.NewString()
	mutatingWin := uuid.NewString()
	gappedWin := uuid.NewString()

	for i, id := range []string{readOnlyWin, mutatingWin, gappedWin} {
		testutil.FailErr(t, "insert window", q.InsertSourceCommandWindow(t.Context(), db.InsertSourceCommandWindowParams{
			ID: id, ProjectID: f.project.ID, SessionID: f.session.ID,
			ToolName: "command", Ordinal: int64(i + 1), StartedTs: "2026-11-26T10:00:00Z",
		}))
		testutil.FailErr(t, "end window", q.EndSourceCommandWindow(t.Context(), db.EndSourceCommandWindowParams{
			ID: id, EndedTs: "2026-11-26T10:00:01Z",
		}))
	}

	// Mutating window is referenced by a source_operations row.
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO source_operations (
		id, project_id, branch_id, origin, person_id, cause, actor_label, session_id,
		job_id, turn, tool_call_id, tool_name, batch_id, operation_key, capture_quality,
		started_ts, committed_ts, command_window_id
	) VALUES (?, ?, '', 'agent', NULL, 'command', 'agent', ?, '', 1, 'call-mut', 'command', '', ?, 'exact',
		'2026-11-26T10:00:00Z', '2026-11-26T10:00:01Z', ?)`,
		uuid.NewString(), f.project.ID, f.session.ID, uuid.NewString(), mutatingWin)
	testutil.FailErr(t, "insert mutating source_operation", err)

	// Gapped window has an incomplete coverage record.
	testutil.FailErr(t, "record coverage gap", q.UpsertSourceCommandWindowCoverage(t.Context(), db.UpsertSourceCommandWindowCoverageParams{
		WindowID: gappedWin, RootID: f.project.Outside.ID, Path: "srv/shared",
		Coverage: "not_observed", Reason: "io_error", BoundariesJson: "[]",
	}))

	// Run backend pruning on all three windows.
	prunedRO, err := q.DeleteSourceCommandWindowIfUnreferenced(t.Context(), readOnlyWin)
	testutil.FailErr(t, "prune read-only window", err)
	prunedMut, err := q.DeleteSourceCommandWindowIfUnreferenced(t.Context(), mutatingWin)
	testutil.FailErr(t, "prune mutating window", err)
	prunedGap, err := q.DeleteSourceCommandWindowIfUnreferenced(t.Context(), gappedWin)
	testutil.FailErr(t, "prune gapped window", err)

	if prunedRO != 1 || prunedMut != 0 || prunedGap != 0 {
		t.Fatalf("pruned counts = (ro=%d, mut=%d, gap=%d), want (1, 0, 0)", prunedRO, prunedMut, prunedGap)
	}

	// Read-only pruned window returns 404 source_history_not_found (expected).
	roResp := f.get(fmt.Sprintf("/v1/projects/%s/source/command-windows/%s?session_id=%s", f.project.ID, readOnlyWin, f.session.ID))
	if roResp.Code != http.StatusNotFound {
		t.Fatalf("pruned read-only window status = %d, want 404: %s", roResp.Code, roResp.Body.String())
	}
	var roErr wire.ErrorResponse
	testutil.FailErr(t, "decode read-only 404", json.Unmarshal(roResp.Body.Bytes(), &roErr))
	if roErr.Code != wire.ApiErrorCodeSourceHistoryNotFound {
		t.Fatalf("pruned read-only window code = %q, want %q", roErr.Code, wire.ApiErrorCodeSourceHistoryNotFound)
	}

	// Mutating window remains readable with settling=false, coverage=[].
	mutResp := f.get(fmt.Sprintf("/v1/projects/%s/source/command-windows/%s?session_id=%s", f.project.ID, mutatingWin, f.session.ID))
	if mutResp.Code != http.StatusOK {
		t.Fatalf("mutating window status = %d, want 200: %s", mutResp.Code, mutResp.Body.String())
	}
	var mutState wire.CommandObservationStatus
	testutil.FailErr(t, "decode mutating state", json.Unmarshal(mutResp.Body.Bytes(), &mutState))
	if mutState.Settling || len(mutState.Coverage) != 0 {
		t.Fatalf("mutating observation = %+v", mutState)
	}

	// Gapped window remains readable with 1 coverage gap.
	gapResp := f.get(fmt.Sprintf("/v1/projects/%s/source/command-windows/%s?session_id=%s", f.project.ID, gappedWin, f.session.ID))
	if gapResp.Code != http.StatusOK {
		t.Fatalf("gapped window status = %d, want 200: %s", gapResp.Code, gapResp.Body.String())
	}
	var gapState wire.CommandObservationStatus
	testutil.FailErr(t, "decode gapped state", json.Unmarshal(gapResp.Body.Bytes(), &gapState))
	if gapState.Settling || len(gapState.Coverage) != 1 || gapState.Coverage[0].Reason != wire.SourceWindowGapIoError {
		t.Fatalf("gapped observation = %+v", gapState)
	}
}
