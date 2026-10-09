package security

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type rewindFixture struct {
	h          *wiring.Harness
	base       string
	sessionID  string
	projectID  string
	projectDir string
}

func newRewindFixture(t *testing.T) rewindFixture {
	t.Helper()
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	projectDir := t.TempDir()
	project := createAPIProjectAtPath(t, httpSrv.URL, projectDir)
	sess := openAPIPostJSON[wire.Session](t, httpSrv.URL, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"spec"}`, http.StatusAccepted)
	return rewindFixture{
		h:          h,
		base:       httpSrv.URL,
		sessionID:  sess.ID,
		projectID:  project.ID,
		projectDir: projectDir,
	}
}

func TestRewindRequiresAuth(t *testing.T) {
	f := newRewindFixture(t)
	for _, suffix := range []string{"/rewind", "/rewind/preview"} {
		t.Run(suffix, func(t *testing.T) {

			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
				f.base+"/v1/sessions/"+f.sessionID+suffix,
				strings.NewReader(`{"operation_id":"`+uuid.NewString()+`","message_id":"`+uuid.NewString()+`"}`))
			testutil.FailErr(t, "build unauthenticated rewind request", err)
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			testutil.FailErr(t, "post rewind without auth", err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			testutil.FailErr(t, "read body", err)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d want 401 — rewind must never be reachable unauthenticated; body = %s",
					resp.StatusCode, body)
			}
			AssertHTTPResponseMatchesOpenAPI(t, resp.StatusCode, resp.Header, body, http.MethodPost,
				"/v1/sessions/{id}"+suffix, map[string]string{"id": f.sessionID})
		})
	}
}

func TestRewindRejectsUnknownAndIneligibleAnchors(t *testing.T) {
	f := newRewindFixture(t)
	ctx := context.Background()

	// Only user turns can anchor a rewind.
	assistantID := uuid.New().String()
	testutil.FailErr(t, "append assistant row", f.h.Store.AppendMessages(ctx, f.sessionID, wire.Message{
		ID:      assistantID,
		Role:    wire.MessageRoleAssistant,
		Content: "a reply",
	}))

	rows := []rewindErrorCase{
		{
			name:       "anchor absent from transcript",
			body:       `{"operation_id":"` + uuid.NewString() + `","message_id":"` + uuid.NewString() + `"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "rewind_anchor_not_found",
		},
		{
			name:       "mode other than before_turn",
			body:       `{"operation_id":"` + uuid.NewString() + `","message_id":"` + uuid.NewString() + `","mode":"after_turn"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "rewind_mode_unsupported",
		},
		{
			name:       "assistant row is not an ask",
			body:       `{"operation_id":"` + uuid.NewString() + `","message_id":"` + assistantID + `"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "rewind_anchor_ineligible",
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			assertRewindError(t, f.base, f.sessionID, row)
		})
	}
}

func TestRewindRefusedWhileSessionBusy(t *testing.T) {
	f := newRewindFixture(t)
	ctx := context.Background()

	userID := uuid.New().String()
	testutil.FailErr(t, "append user ask", f.h.Store.AppendMessages(ctx, f.sessionID, wire.Message{
		ID:      userID,
		Role:    wire.MessageRoleUser,
		Content: "do the thing",
	}))
	// Rewind requires an idle session to avoid racing file writes.
	testutil.FailErr(t, "mark busy", f.h.Store.SetSessionStatus(ctx, f.sessionID, wire.SessionStatusBusy))

	assertRewindError(t, f.base, f.sessionID, rewindErrorCase{
		name:       "busy session refuses rewind",
		body:       `{"operation_id":"` + uuid.NewString() + `","message_id":"` + userID + `"}`,
		wantStatus: http.StatusConflict,
		wantCode:   "session_not_idle",
	})
}

func TestRewindRestoresFilesAndTruncatesTranscript(t *testing.T) {
	f := newRewindFixture(t)
	ctx := context.Background()

	target := filepath.Join(f.projectDir, "note.txt")
	testutil.FailErr(t, "seed file", os.WriteFile(target, []byte("before"), 0o644))

	// Prompt execution captures the checkpoint for its visible user row.
	acceptPromptOpenAPI(t, f.base, f.sessionID, `{"text":"change the note"}`)
	anchorID := lastVisibleUserMessageID(t, f.base, f.sessionID)

	// Mutation capture retains the bytes from before the write.
	f.h.SessionMgr.Chats.Captures.RecordPrimaryMutation(ctx, f.sessionID, "note.txt")
	testutil.FailErr(t, "write file", os.WriteFile(target, []byte("after"), 0o644))
	var dbPath, rootID string
	testutil.FailErr(t, "resolve source storage", f.h.DB.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath))
	testutil.FailErr(t, "resolve project root", f.h.DB.QueryRowContext(ctx, `SELECT id FROM project_roots WHERE project_id=? AND is_primary=1`, f.projectID).Scan(&rootID))
	ledger := sourceledger.New(f.h.DB, filepath.Join(filepath.Dir(dbPath), enginepaths.SourceContentDirName))
	turn, err := f.h.Store.UserTurnOrdinal(ctx, f.sessionID)
	testutil.FailErr(t, "resolve source turn", err)
	testutil.FailErr(t, "record agent effect", ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "note.txt"}, ProjectID: f.projectID, Origin: wire.SourceChangeOriginAgent, Op: wire.SourceChangeOpWrite, SessionID: f.sessionID, Turn: turn, Before: []byte("before"), After: []byte("after")}))

	before := listMessagesOpenAPI(t, f.base, map[string]string{"id": f.sessionID}, http.StatusOK)

	operationID := uuid.NewString()
	preview := openAPIPostJSON[wire.RewindPreviewResponse](t, f.base, "/v1/sessions/{id}/rewind/preview", map[string]string{"id": f.sessionID}, `{"message_id":"`+anchorID+`"}`, http.StatusOK)
	bodyJSON := `{"operation_id":"` + operationID + `","message_id":"` + anchorID + `","plan_digest":"` + preview.PlanDigest + `"}`
	got := openAPIPostJSON[wire.RewindSessionResponse](t, f.base, "/v1/sessions/{id}/rewind",
		map[string]string{"id": f.sessionID}, bodyJSON, http.StatusOK)

	if got.RestoredPrompt != "change the note" {
		t.Fatalf("restored_prompt = %q, want the anchor ask so Edit can preload the composer", got.RestoredPrompt)
	}
	body, err := os.ReadFile(target)
	testutil.FailErr(t, "read restored file", err)
	if string(body) != "before" {
		t.Fatalf("file = %q, want the pre-turn bytes restored", body)
	}
	after := listMessagesOpenAPI(t, f.base, map[string]string{"id": f.sessionID}, http.StatusOK)
	if len(after) >= len(before) {
		t.Fatalf("transcript went %d → %d, want rows removed", len(before), len(after))
	}
	if got.TruncatedMessageCount != len(before)-len(after) {
		t.Fatalf("truncated_message_count = %d but the transcript lost %d rows",
			got.TruncatedMessageCount, len(before)-len(after))
	}
	for _, m := range after {
		if m.ID == anchorID {
			t.Fatal("the anchor ask survived its own rewind")
		}
	}

	// Operation replay returns the stored result after its anchor has been removed.
	replayed := openAPIPostJSON[wire.RewindSessionResponse](t, f.base, "/v1/sessions/{id}/rewind",
		map[string]string{"id": f.sessionID}, bodyJSON, http.StatusOK)
	if !reflect.DeepEqual(replayed, got) {
		t.Fatalf("rewind replay = %+v, want exact %+v", replayed, got)
	}
	assertRewindError(t, f.base, f.sessionID, rewindErrorCase{
		name:       "operation id reused for another anchor",
		body:       `{"operation_id":"` + operationID + `","message_id":"` + uuid.NewString() + `"}`,
		wantStatus: http.StatusConflict,
		wantCode:   "idempotency_conflict",
	})
}

func TestRewindWillNotUseAnotherSessionsAnchor(t *testing.T) {
	f := newRewindFixture(t)
	ctx := context.Background()

	other := openAPIPostJSON[wire.Session](t, f.base, "/v1/sessions", nil,
		`{"project_id":"`+f.projectID+`","posture":"spec"}`, http.StatusAccepted)
	foreignAnchor := uuid.New().String()
	testutil.FailErr(t, "append ask to the other session",
		f.h.Store.AppendMessages(ctx, other.ID, wire.Message{
			ID:      foreignAnchor,
			Role:    wire.MessageRoleUser,
			Content: "someone else's ask",
		}))

	assertRewindError(t, f.base, f.sessionID, rewindErrorCase{
		name:       "anchor belongs to a different session",
		body:       `{"operation_id":"` + uuid.NewString() + `","message_id":"` + foreignAnchor + `"}`,
		wantStatus: http.StatusNotFound,
		wantCode:   "rewind_anchor_not_found",
	})

	msgs, err := f.h.Store.GetMessages(ctx, other.ID)
	testutil.FailErr(t, "get other session messages", err)
	found := false
	for _, m := range msgs {
		if m.ID == foreignAnchor {
			found = true
		}
	}
	if !found {
		t.Fatal("the other session's ask was removed by a rewind aimed at this one")
	}
}

func TestRewindIsNotAToolSurface(t *testing.T) {
	f := newRewindFixture(t)
	forbidden := map[string]struct{}{
		"rewind": {}, "rewind_session": {}, "undo": {}, "undo_turn": {}, "restore_checkpoint": {},
	}
	for _, meta := range f.h.ToolRegistry.List() {
		if _, bad := forbidden[meta.Name]; bad {
			t.Fatalf("tool %q is registered — rewind is host-managed and must stay off the agent loop", meta.Name)
		}
	}
}

func lastVisibleUserMessageID(t *testing.T, base, sessionID string) string {
	t.Helper()
	msgs := listMessagesOpenAPI(t, base, map[string]string{"id": sessionID}, http.StatusOK)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == wire.MessageRoleUser && msgs[i].Visibility != wire.MessageVisibilityInternal {
			return msgs[i].ID
		}
	}
	t.Fatal("no visible user message to anchor a rewind")
	return ""
}

type rewindErrorCase struct {
	name       string
	body       string
	wantStatus int
	wantCode   string
}

func assertRewindError(t *testing.T, base, sessionID string, row rewindErrorCase) {
	t.Helper()
	var input map[string]any
	testutil.FailErr(t, "decode refusal fixture", json.Unmarshal([]byte(row.body), &input))
	input["plan_digest"] = "unused-refusal-plan"
	encoded, err := json.Marshal(input)
	testutil.FailErr(t, "encode refusal fixture", err)
	row.body = string(encoded)
	raw := openAPIDo(t, base, http.MethodPost, "/v1/sessions/{id}/rewind",
		map[string]string{"id": sessionID}, row.body, row.wantStatus)
	got := decodeOpenAPI[wire.ErrorResponse](t, http.MethodPost, "/v1/sessions/{id}/rewind", raw)
	if string(got.Code) != row.wantCode {
		t.Fatalf("%s: code = %q, want %q (body=%s)", row.name, got.Code, row.wantCode, raw)
	}
}
