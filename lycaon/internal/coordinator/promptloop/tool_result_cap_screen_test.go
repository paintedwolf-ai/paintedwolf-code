package promptloop

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	storepkg "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/zstdcodec"
	"github.com/lycaon/lycaon/pkg/api"
)

// shortestRecognizableFragment is this test's leak-detection floor.
const shortestRecognizableFragment = 12

// secretFragments returns prefixes and suffixes above the test floor.
func secretFragments(secret string) []string {
	var out []string
	for n := shortestRecognizableFragment; n < len(secret); n++ {
		out = append(out, secret[:n], secret[len(secret)-n:])
	}
	return out
}

// cappedToolResultWithSecretAtTheCut places the value across the inline cap.
func cappedToolResultWithSecretAtTheCut(maxBytes int) string {
	head := strings.Repeat("a", maxBytes-len(storageBoundarySecret))
	return head + storageBoundarySecret + "\n" + strings.Repeat("payload\n", 512)
}

func TestCappedToolResultLeavesNoSecretFragmentAnywhere(t *testing.T) {
	const maxBytes = 4096
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

	var appended []api.Message
	loop := NewPromptLoop(PromptLoopDeps{
		Tools: ToolsDeps{
			DataDir: dir,
		},
		Closeout: CloseoutDeps{
			HintConfig: loadCoordinatorTestHintConfig(t),
		},
		Projection: ProjectionDeps{
			RedactMessageForStorage: testStorageRedactor,
			AppendMessages: func(ctx context.Context, sessionID string, msgs ...api.Message) error {
				appended = append(appended, msgs...)
				return sqlStore.AppendMessages(ctx, sessionID, msgs...)
			},
			UpdateMessage: func(ctx context.Context, sessionID, messageID string, msg api.Message) error {
				_, updateErr := sqlStore.UpdateMessage(ctx, sessionID, messageID, msg)
				return updateErr
			},
		},
	})

	rawContent := cappedToolResultWithSecretAtTheCut(maxBytes)
	// Verify the fixture crosses the cap.
	if !carriesSecretFragment(rawContent[:maxBytes]) {
		t.Fatal("fixture no longer cuts through the credential")
	}
	rawArgs := map[string]any{"path": ".env"}
	projection := loop.Projection.projectToolResultForStorage(context.Background(), rawContent, rawArgs)
	projected := loop.Tools.truncateToolResultForSession(
		context.Background(), "read", projection, rawContent, maxBytes, 0, sess,
	)
	if !strings.Contains(projected.content, "Code: TOOL_OUTPUT_TRUNCATED") {
		t.Fatalf("test does not exercise the inline cap: %q", projected.content[:min(len(projected.content), 200)])
	}
	assertNoSecretFragment(t, "model-visible tool result", projected.content)

	toolMsg := api.Message{
		ID:      "tool-cap-1",
		Role:    api.MessageRoleTool,
		Content: projected.content,
		ToolResult: &api.ToolResult{
			Tool:     "read",
			Content:  projected.content,
			ToolArgs: projection.args,
			Outcome:  api.ToolResultOutcomeCompleted,
		},
	}
	last := time.Time{}
	_, err = loop.Batch.persistClassifiedToolOutcome(
		context.Background(), sess.ID, sess, nil, toolCallOutcome{
			toolName: "read", toolArgs: rawArgs, toolMsg: toolMsg,
		}, &last, &promptLoopTurnState{},
	)
	testutil.FailErr(t, "commit capped tool result", err)
	for _, msg := range appended {
		assertNoSecretFragment(t, "appended transcript row", msg.Content)
		if msg.ToolResult != nil {
			assertNoSecretFragment(t, "appended tool result", msg.ToolResult.Content)
		}
	}

	for _, fragment := range secretFragments(storageBoundarySecret) {
		var rows int
		testutil.FailErr(t, "query transcript and FTS", sqlDB.QueryRowContext(context.Background(), `
			SELECT
				(SELECT count(*) FROM messages WHERE instr(content, ?) > 0 OR instr(tool_result_json, ?) > 0) +
				(SELECT count(*) FROM messages_fts WHERE instr(content, ?) > 0)
		`, fragment, fragment, fragment).Scan(&rows))
		if rows != 0 {
			t.Fatalf("credential fragment %q reached %d transcript or FTS rows", fragment, rows)
		}
	}

	spillPaths, err := filepath.Glob(filepath.Join(dir, "projects", testdbseed.DefaultProjectID, "tool-output", "*.txt"))
	testutil.FailErr(t, "find tool-result spill", err)
	if len(spillPaths) != 1 {
		t.Fatalf("spill files = %d want 1", len(spillPaths))
	}
	stored, err := os.ReadFile(spillPaths[0])
	testutil.FailErr(t, "read tool-result spill", err)
	spill, err := zstdcodec.Decompress(stored)
	if err != nil {
		spill = stored
	}
	for _, fragment := range secretFragments(storageBoundarySecret) {
		if bytes.Contains(spill, []byte(fragment)) {
			t.Fatalf("credential fragment %q reached the spill file %s", fragment, spillPaths[0])
		}
	}
}

func carriesSecretFragment(content string) bool {
	for _, fragment := range secretFragments(storageBoundarySecret) {
		if strings.Contains(content, fragment) {
			return true
		}
	}
	return false
}

func assertNoSecretFragment(t *testing.T, where, content string) {
	t.Helper()
	for _, fragment := range secretFragments(storageBoundarySecret) {
		if strings.Contains(content, fragment) {
			t.Fatalf("%s carries credential fragment %q", where, fragment)
		}
	}
}
