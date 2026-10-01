package promptloop

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	storepkg "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const storageBoundarySecret = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"

func testStorageRedactor(_ context.Context, msg api.Message) (api.Message, bool) {
	out := msg
	out.Content = strings.ReplaceAll(msg.Content, storageBoundarySecret, "[REDACTED]")
	if len(msg.ContentParts) > 0 {
		out.ContentParts = append([]api.MessageContentPart(nil), msg.ContentParts...)
		for i := range out.ContentParts {
			out.ContentParts[i].Content = strings.ReplaceAll(out.ContentParts[i].Content, storageBoundarySecret, "[REDACTED]")
		}
	}
	if msg.ToolResult != nil {
		clone := *msg.ToolResult
		clone.Content = strings.ReplaceAll(msg.ToolResult.Content, storageBoundarySecret, "[REDACTED]")
		clone.ToolArgs = make(map[string]any, len(msg.ToolResult.ToolArgs))
		for key, value := range msg.ToolResult.ToolArgs {
			if text, ok := value.(string); ok {
				value = strings.ReplaceAll(text, storageBoundarySecret, "[REDACTED]")
			}
			clone.ToolArgs[key] = value
		}
		out.ToolResult = &clone
	}
	if len(msg.ToolCalls) > 0 {
		out.ToolCalls = append([]api.ToolCall(nil), msg.ToolCalls...)
		for i := range out.ToolCalls {
			out.ToolCalls[i].Args = redactTestStorageMap(out.ToolCalls[i].Args)
			out.ToolCalls[i].ExtraContent = redactTestStorageMap(out.ToolCalls[i].ExtraContent)
		}
	}
	changed := out.Content != msg.Content ||
		(len(msg.ContentParts) > 0 && out.ContentParts[0].Content != msg.ContentParts[0].Content) ||
		(out.ToolResult != nil && msg.ToolResult != nil &&
			(out.ToolResult.Content != msg.ToolResult.Content ||
				out.ToolResult.ToolArgs["token"] != msg.ToolResult.ToolArgs["token"])) ||
		(len(msg.ToolCalls) > 0 && out.ToolCalls[0].Args["content"] != msg.ToolCalls[0].Args["content"])
	if changed {
		out.HostSecretRedaction = api.NewHostSecretRedactionMeta([]api.RedactedSpan{{Field: "content", Start: 0, Length: 10, Kind: api.RedactionKindSecret}})
	}
	return out, changed
}

func redactTestStorageMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		if text, ok := value.(string); ok {
			value = strings.ReplaceAll(text, storageBoundarySecret, "[REDACTED]")
		}
		out[key] = value
	}
	return out
}

func TestToolResultSpillUsesStorageProjection(t *testing.T) {
	dataDir := t.TempDir()
	rawContent := "token=" + storageBoundarySecret + "\n" + strings.Repeat("payload\n", 1024)
	rawArgs := map[string]any{"path": ".env", "token": storageBoundarySecret}
	loop := &PromptLoop{Deps: PromptLoopDeps{
		DataDir:                 dataDir,
		HintConfig:              loadCoordinatorTestHintConfig(t),
		RedactMessageForStorage: testStorageRedactor,
	}}

	projection := loop.projectToolResultForStorage(context.Background(), rawContent, rawArgs)
	preview := loop.truncateToolResultForSession(
		t.Context(),
		"read",
		projection,
		rawContent,
		512,
		0,
		&api.Session{ID: "session-1", ProjectID: "project-1"},
	)
	// A capped result is cut from the storage projection, so the inline copy the
	// model and the transcript receive can only carry screened bytes.
	if strings.Contains(preview.content, storageBoundarySecret) {
		t.Fatalf("capped preview kept the raw credential: %q", preview.content)
	}
	if !strings.Contains(preview.content, "[REDACTED]") {
		t.Fatalf("capped preview is not the storage projection: %q", preview.content)
	}
	if token, _ := projection.args["token"].(string); strings.Contains(token, storageBoundarySecret) {
		t.Fatalf("durable arguments retained raw credential: %q", token)
	}

	hostDataDir := filepath.Join(dataDir, "projects", "project-1")
	spillPaths, err := filepath.Glob(filepath.Join(hostDataDir, "tool-output", "*.txt"))
	testutil.FailErr(t, "find tool-result spill", err)
	if len(spillPaths) != 1 {
		t.Fatalf("spill files = %d want 1", len(spillPaths))
	}
	spill, err := os.ReadFile(spillPaths[0])
	testutil.FailErr(t, "read tool-result spill", err)
	if bytes.Contains(spill, []byte(storageBoundarySecret)) {
		t.Fatalf("raw credential reached spill file %s", spillPaths[0])
	}
	if !bytes.Contains(spill, []byte("[REDACTED]")) {
		t.Fatalf("spill file does not contain the storage-safe projection: %q", spill[:min(len(spill), 200)])
	}
}

func TestTransientMessageRestoresEveryScreenedField(t *testing.T) {
	stored := api.Message{
		ID:           "message-1",
		Content:      "token=[REDACTED]",
		ContentParts: []api.MessageContentPart{{Content: "part=[REDACTED]"}},
		ToolCalls: []api.ToolCall{{
			Args:         map[string]any{"token": "[REDACTED]"},
			ExtraContent: map[string]any{"token": "[REDACTED]"},
		}},
		ToolResult: &api.ToolResult{
			Content:  "result=[REDACTED]",
			ToolArgs: map[string]any{"token": "[REDACTED]"},
		},
		HostSecretRedaction: api.NewHostSecretRedactionMeta([]api.RedactedSpan{{Field: "content", Start: 0, Length: 10, Kind: api.RedactionKindSecret}, {Field: "content", Start: 12, Length: 10, Kind: api.RedactionKindSecret}, {Field: "content", Start: 24, Length: 10, Kind: api.RedactionKindSecret}, {Field: "content", Start: 36, Length: 10, Kind: api.RedactionKindSecret}, {Field: "content", Start: 48, Length: 10, Kind: api.RedactionKindSecret}}),
	}
	raw := stored
	raw.Content = "token=" + storageBoundarySecret
	raw.ContentParts = []api.MessageContentPart{{Content: "part=" + storageBoundarySecret}}
	raw.ToolCalls = []api.ToolCall{{
		Args:         map[string]any{"token": storageBoundarySecret},
		ExtraContent: map[string]any{"token": storageBoundarySecret},
	}}
	raw.ToolResult = &api.ToolResult{
		Content:  "result=" + storageBoundarySecret,
		ToolArgs: map[string]any{"token": storageBoundarySecret},
	}
	raw.HostSecretRedaction = nil

	got := transientMessageFromStored(stored, &raw)
	fields := []string{
		got.Content,
		got.ContentParts[0].Content,
		got.ToolCalls[0].Args["token"].(string),
		got.ToolCalls[0].ExtraContent["token"].(string),
		got.ToolResult.Content,
		got.ToolResult.ToolArgs["token"].(string),
	}
	for _, field := range fields {
		if !strings.Contains(field, storageBoundarySecret) {
			t.Fatalf("transient field was not restored: %+v", got)
		}
	}
	if got.HostSecretRedaction != nil {
		t.Fatalf("raw transient retained durable provenance: %+v", got.HostSecretRedaction)
	}
}

func TestToolResultSecretIsRedactedBeforeEveryDurableConsumer(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "store.db")
	sqlDB := testdbfixture.OpenPath(t, dbPath)
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	sqlStore := storepkg.NewSQL(sqlDB)
	sess, err := sqlStore.Create(context.Background(), api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	sess.WorkspacePath = dir

	var evidence, evidenceArg, compacted string
	var appended []api.Message
	loop := &PromptLoop{Deps: PromptLoopDeps{
		RedactMessageForStorage: testStorageRedactor,
		CommitEvidenceToolResult: func(ctx context.Context, sessionID string, _ *api.Session, toolName string, args map[string]any, content, _ string) (string, string, error) {
			evidence = content
			evidenceArg, _ = args["token"].(string)
			return sqlStore.CommitEvidenceToolResult(ctx, sessionID, dir, toolName, args, content)
		},
		CompactToolWire: func(_ context.Context, _ *api.Session, _ string, content string, _ compaction.CompactToolWireOpts) (string, *api.CompactedChunkMeta) {
			compacted = content
			return content, nil
		},
		AppendMessages: func(ctx context.Context, sessionID string, msgs ...api.Message) error {
			appended = append(appended, msgs...)
			return sqlStore.AppendMessages(ctx, sessionID, msgs...)
		},
		UpdateMessage: func(ctx context.Context, sessionID, messageID string, msg api.Message) error {
			_, err := sqlStore.UpdateMessage(ctx, sessionID, messageID, msg)
			return err
		},
	}}
	raw := api.Message{
		ID:      "tool-1",
		Role:    api.MessageRoleTool,
		Content: "token=" + storageBoundarySecret,
		ContentParts: []api.MessageContentPart{{
			Content: "token=" + storageBoundarySecret,
			Origin:  api.MessageOriginTool,
		}},
		ToolResult: &api.ToolResult{
			Tool:     "read",
			Content:  "token=" + storageBoundarySecret,
			ToolArgs: map[string]any{"path": ".env", "token": storageBoundarySecret},
			Outcome:  api.ToolResultOutcomeCompleted,
		},
	}
	last := time.Time{}
	st := &promptLoopTurnState{}
	history, err := loop.persistClassifiedToolOutcome(
		context.Background(), sess.ID, sess, nil, toolCallOutcome{
			toolName:       "read",
			toolArgs:       raw.ToolResult.ToolArgs,
			toolMsg:        raw,
			handleEligible: true,
			agentNote: &tools.AgentNoteCapture{
				MessageID: "note-1",
				Content:   "observed " + storageBoundarySecret,
			},
		}, &last, st,
	)
	testutil.FailErr(t, "commit storage-safe messages", err)
	st.history = history

	if strings.Contains(evidence, storageBoundarySecret) || strings.Contains(evidenceArg, storageBoundarySecret) ||
		strings.Contains(compacted, storageBoundarySecret) {
		t.Fatalf("pre-store consumer saw raw secret: evidence=%q arg=%q compacted=%q", evidence, evidenceArg, compacted)
	}
	for _, msg := range appended {
		if strings.Contains(msg.Content, storageBoundarySecret) ||
			(len(msg.ContentParts) > 0 && strings.Contains(msg.ContentParts[0].Content, storageBoundarySecret)) ||
			(msg.ToolResult != nil && strings.Contains(msg.ToolResult.Content, storageBoundarySecret)) {
			t.Fatalf("append received raw secret: %+v", msg)
		}
	}
	if len(history) != 2 || !strings.Contains(history[0].Content, storageBoundarySecret) ||
		!strings.Contains(history[1].Content, storageBoundarySecret) ||
		!strings.Contains(history[0].ContentParts[0].Content, storageBoundarySecret) ||
		!strings.Contains(history[0].ToolResult.ToolArgs["token"].(string), storageBoundarySecret) {
		t.Fatalf("next-turn transient overlay missing: %+v", history)
	}
	if history[0].HostSecretRedaction != nil || history[1].HostSecretRedaction != nil {
		t.Fatalf("raw overlay inherited stored-copy provenance: %+v", history)
	}
	if len(st.secretStorageOverlays) != 2 {
		t.Fatalf("overlays = %d want 2", len(st.secretStorageOverlays))
	}
	var rawRows int
	testutil.FailErr(t, "query transcript and FTS", sqlDB.QueryRowContext(context.Background(), `
		SELECT
			(SELECT count(*) FROM messages WHERE instr(content, ?) > 0 OR instr(content_parts_json, ?) > 0 OR instr(tool_result_json, ?) > 0) +
			(SELECT count(*) FROM messages_fts WHERE instr(content, ?) > 0) +
			(SELECT count(*) FROM evidence_fts WHERE instr(snippet, ?) > 0 OR instr(path, ?) > 0 OR instr(url, ?) > 0)
	`, storageBoundarySecret, storageBoundarySecret, storageBoundarySecret, storageBoundarySecret,
		storageBoundarySecret, storageBoundarySecret, storageBoundarySecret).Scan(&rawRows))
	if rawRows != 0 {
		t.Fatalf("raw secret reached transcript or FTS rows: %d", rawRows)
	}
	for _, path := range []string{dbPath, dbPath + "-wal"} {
		data, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			testutil.FailErr(t, "read sqlite file", readErr)
		}
		if bytes.Contains(data, []byte(storageBoundarySecret)) {
			t.Fatalf("raw secret reached SQLite storage file %s", path)
		}
	}

	canonical := append([]api.Message(nil), appended...)
	canonical[0].DietStamp = "compacted-after-commit"
	reloaded := st.applySecretStorageOverlays(canonical)
	if !strings.Contains(reloaded[0].Content, storageBoundarySecret) {
		t.Fatalf("reload did not restore one-turn overlay: %+v", reloaded)
	}
	if reloaded[0].DietStamp != "compacted-after-commit" {
		t.Fatalf("reload restored stale transient metadata: %+v", reloaded[0])
	}
	st.clearSecretStorageOverlays()
	if strings.Contains(st.history[0].Content, storageBoundarySecret) ||
		strings.Contains(st.history[1].Content, storageBoundarySecret) || len(st.secretStorageOverlays) != 0 {
		t.Fatalf("raw overlay survived decision: history=%+v overlays=%d", st.history, len(st.secretStorageOverlays))
	}
	if st.history[0].HostSecretRedaction == nil || st.history[1].HostSecretRedaction == nil {
		t.Fatalf("canonical redaction provenance was not restored: %+v", st.history)
	}
	if st.history[0].DietStamp != "compacted-after-commit" {
		t.Fatalf("clear restored stale pre-compaction metadata: %+v", st.history[0])
	}
}

type capturingSecretOverlayLLM struct {
	requests []modelcall.CompletionRequest
}

func (c *capturingSecretOverlayLLM) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return &modelcall.Completion{Content: "ok"}, nil
}

func (c *capturingSecretOverlayLLM) Stream(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c.requests = append(c.requests, req)
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: "ok", Done: true}
	close(ch)
	return ch, nil
}

func TestSecretStorageOverlayIsVisibleToExactlyOneModelRequest(t *testing.T) {
	client := &capturingSecretOverlayLLM{}
	stored := api.Message{ID: "tool-1", Role: api.MessageRoleTool, Content: "token=[REDACTED]"}
	transient := api.Message{ID: "tool-1", Role: api.MessageRoleTool, Content: "token=" + storageBoundarySecret}
	st := &promptLoopTurnState{history: []api.Message{transient}}
	st.rememberSecretStorageOverlay(stored, transient)
	loop := &PromptLoop{Deps: PromptLoopDeps{
		LLM: client,
		BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
			return history, nil
		},
	}}
	sess := &api.Session{ID: "sess", ParentSessionID: "parent"}
	_, _, err := loop.completeStream(context.Background(), sess, sess.ID, st.history, "worker", "", 0, 2, false, st, nil)
	testutil.FailErr(t, "first model request", err)
	_, _, err = loop.completeStream(context.Background(), sess, sess.ID, st.history, "worker", "", 1, 2, false, st, nil)
	testutil.FailErr(t, "second model request", err)
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d want 2", len(client.requests))
	}
	if !strings.Contains(client.requests[0].Messages[0].Content, storageBoundarySecret) {
		t.Fatalf("first request missed transient secret: %+v", client.requests[0].Messages)
	}
	if strings.Contains(client.requests[1].Messages[0].Content, storageBoundarySecret) ||
		client.requests[1].Messages[0].Content != stored.Content {
		t.Fatalf("second request retained transient secret: %+v", client.requests[1].Messages)
	}
}

type assistantSecretOverlayLLM struct {
	requests []modelcall.CompletionRequest
}

func (c *assistantSecretOverlayLLM) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return &modelcall.Completion{Content: "ok"}, nil
}

func (c *assistantSecretOverlayLLM) Stream(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c.requests = append(c.requests, req)
	ch := make(chan modelcall.StreamChunk, 1)
	if len(c.requests) == 1 {
		ch <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{{
			ID: "call-write", Name: "write",
			Args: map[string]any{"path": "Dockerfile", "content": "TOKEN=" + storageBoundarySecret},
		}}, Done: true}
	} else {
		ch <- modelcall.StreamChunk{Content: "ok", Done: true}
	}
	close(ch)
	return ch, nil
}

func TestAssistantToolCallSecretIsStoredRedactedAndSentOnce(t *testing.T) {
	client := &assistantSecretOverlayLLM{}
	var updates []api.Message
	loop := &PromptLoop{Deps: PromptLoopDeps{
		LLM:                     client,
		RedactMessageForStorage: testStorageRedactor,
		BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
			return history, nil
		},
		AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error { return nil },
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			updates = append(updates, msg)
			return nil
		},
	}}
	st := &promptLoopTurnState{}
	sess := &api.Session{ID: "sess", ParentSessionID: "parent"}
	assistant, completion, _, err := loop.runAssistantStreamTurn(
		context.Background(), sess.ID, sess, nil, st, "worker", "", 0, 3, false,
	)
	testutil.FailErr(t, "assistant tool-call turn", err)
	if len(completion.ToolCalls) != 1 || !messageCarriesStorageSecret(assistant) {
		t.Fatalf("execution copy lost raw tool call: %+v", assistant.ToolCalls)
	}
	if len(updates) == 0 || messageCarriesStorageSecret(updates[len(updates)-1]) {
		t.Fatalf("stored assistant update retained raw tool call: %+v", updates)
	}
	if updates[len(updates)-1].HostSecretRedaction == nil {
		t.Fatalf("stored assistant update lacks redaction provenance: %+v", updates[len(updates)-1])
	}
	canonical := updates[len(updates)-1]
	canonical.Visibility = api.MessageVisibilityTranscript
	st.history = st.applySecretStorageOverlays([]api.Message{canonical})
	if st.history[0].Visibility != api.MessageVisibilityTranscript || !messageCarriesStorageSecret(st.history[0]) {
		t.Fatalf("reloaded assistant overlay lost canonical metadata or raw args: %+v", st.history[0])
	}

	_, _, err = loop.completeStream(context.Background(), sess, sess.ID, st.history, "worker", "", 1, 3, false, st, nil)
	testutil.FailErr(t, "first follow-up request", err)
	_, _, err = loop.completeStream(context.Background(), sess, sess.ID, st.history, "worker", "", 2, 3, false, st, nil)
	testutil.FailErr(t, "second follow-up request", err)
	if len(client.requests) != 3 {
		t.Fatalf("requests = %d want 3", len(client.requests))
	}
	if !messageCarriesStorageSecret(client.requests[1].Messages[0]) {
		t.Fatalf("first follow-up missed one-request raw overlay: %+v", client.requests[1].Messages)
	}
	if messageCarriesStorageSecret(client.requests[2].Messages[0]) {
		t.Fatalf("second follow-up retained raw tool call: %+v", client.requests[2].Messages)
	}
}

func messageCarriesStorageSecret(msg api.Message) bool {
	for _, call := range msg.ToolCalls {
		for _, value := range call.Args {
			if text, ok := value.(string); ok && strings.Contains(text, storageBoundarySecret) {
				return true
			}
		}
	}
	return false
}
