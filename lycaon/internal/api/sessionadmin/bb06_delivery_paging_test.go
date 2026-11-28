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
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/activitybriefing"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/api/briefingadmin"
	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefinghost"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var bb06GuidanceOnce sync.Once

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
	hub      *events.MemoryHub
	pub      *events.Publisher
}

func newBB06Fixture(t *testing.T) *bb06Fixture {
	t.Helper()
	bb06GuidanceOnce.Do(guidancetestsetup.Install)

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
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}

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
		Host: api.HostDependencies{
			Events:         hub,
			EventPublisher: pub,
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
		hub:      hub,
		pub:      pub,
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
			id: execIDs[i], nodeID: "n1", ordinal: i + 1, parentExecID: p.rootID,
			operationID: "grep", state: "completed", owner: "filesystem",
			lifecycle: "read_only", nativeCommand: "grep", effectMode: "read_only",
			effectClasses: `["read_only"]`, canonical: fmt.Sprintf("grep pattern file-%d.go", i+1),
			statusCode: &zero, ownerStarted: 1,
		})
	}
	testutil.FailErr(t, "settle program", f.recorder.SettleProgram(t.Context(), p.programID, toolcontract.ProgramSettlement{
		State: "completed", Status: &zero, DetailReference: "output:done",
	}))

	activityID := execIDs[0]
	for _, limit := range []int{1, 2} {
		for _, scoped := range []bool{false, true} {
			t.Run(fmt.Sprintf("limit=%d/scoped=%v", limit, scoped), func(t *testing.T) {
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
				if pages <= 2 || len(collected) != len(execIDs) {
					t.Fatalf("pages=%d collected=%d want >2 pages and %d executions", pages, len(collected), len(execIDs))
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
