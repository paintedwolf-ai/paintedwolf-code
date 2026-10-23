package conformance_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type fixtureCorpus struct {
	SchemaVersion   int               `json:"schema_version"`
	SeedTextFiles   map[string]string `json:"seed_text_files"`
	SeedBinaryFiles map[string]string `json:"seed_binary_files"`
	SeedGitFiles    map[string]string `json:"seed_git_files"`
	GNUEnvironment  string            `json:"gnu_environment"`
	BSDEnvironment  string            `json:"bsd_environment"`
	Cases           []fixtureCase     `json:"cases"`
}

type fixtureCase struct {
	ID           string          `json:"id"`
	Category     string          `json:"category"`
	Description  string          `json:"description"`
	Commands     []string        `json:"commands"`
	Forms        []string        `json:"forms"`
	Program      string          `json:"program"`
	TrackedFiles []string        `json:"tracked_files"`
	NeedsGit     bool            `json:"needs_git"`
	GNU          fixtureSnapshot `json:"gnu"`
	BSD          fixtureSnapshot `json:"bsd"`
}

type fixtureSnapshot struct {
	Status int                `json:"status"`
	Stdout string             `json:"stdout"`
	Stderr string             `json:"stderr"`
	Files  map[string]*string `json:"files"`
}

type runtimeObservation struct {
	Outcome        command.ProgramOutcome
	TopLevelReject string
	Files          map[string]*string
	ChildReceipts  []string
}

type conformanceRunner struct {
	h      *wiring.Harness
	corpus fixtureCorpus
}

func loadFixtureCorpus(t *testing.T) fixtureCorpus {
	t.Helper()
	path := filepath.Join(configlayout.FindModuleRoot(), "test", "wiring", "testdata", "command_conformance", "fixtures.json")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read conformance fixtures", err)
	var corpus fixtureCorpus
	testutil.FailErr(t, "decode conformance fixtures", json.Unmarshal(raw, &corpus))
	if len(corpus.Cases) == 0 {
		t.Fatal("conformance fixture corpus is empty")
	}
	return corpus
}

func newConformanceRunner(t *testing.T) *conformanceRunner {
	t.Helper()
	corpus := loadFixtureCorpus(t)
	entries := make([]llm.MockResponseEntry, 0, len(corpus.Cases))
	for _, c := range corpus.Cases {
		entries = append(entries, llm.MockResponseEntry{
			Pattern:      "run case " + c.ID,
			ToolCalls:    []llm.MockToolCall{{ID: "call-" + c.ID, Name: "command", Args: map[string]any{"command": c.Program}}},
			FollowUpText: "Case finished.",
		})
	}
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: entries})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	neverAsk := true
	testutil.FailErr(t, "enable never_ask", h.Server.Admin.Settings.Service.Approvals.PutGlobal(settings.ApprovalConfig{
		Posture:  gate.PostureLight,
		NeverAsk: &neverAsk,
	}))
	h.Execution.Host.Executor.Boundary.SetSessionLoopbackGrant(func(context.Context, string, string) (bool, []uint16) {
		return true, nil
	})
	m, err := h.Workflows.Manager.Resolver.ForSession(t.Context(), "", "", "implement", "1.0.0")
	testutil.FailErr(t, "resolve implement manifest", err)
	m.Controls.ContentReview = nil
	h.RegisterManifest(m)
	return &conformanceRunner{h: h, corpus: corpus}
}

func (r *conformanceRunner) seedCaseWorkspace(t *testing.T, c fixtureCase) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range r.corpus.SeedTextFiles {
		target := filepath.Join(dir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir seed text parent", os.MkdirAll(filepath.Dir(target), 0o755))
		testutil.FailErr(t, "write seed text file", os.WriteFile(target, []byte(content), 0o644))
	}
	for rel, encoded := range r.corpus.SeedBinaryFiles {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		testutil.FailErr(t, "decode seed binary file", err)
		target := filepath.Join(dir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir seed binary parent", os.MkdirAll(filepath.Dir(target), 0o755))
		testutil.FailErr(t, "write seed binary file", os.WriteFile(target, decoded, 0o644))
	}
	if c.NeedsGit {
		for rel, encoded := range r.corpus.SeedGitFiles {
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			testutil.FailErr(t, "decode seed git file", err)
			target := filepath.Join(dir, filepath.FromSlash(rel))
			testutil.FailErr(t, "mkdir seed git parent", os.MkdirAll(filepath.Dir(target), 0o755))
			testutil.FailErr(t, "write seed git file", os.WriteFile(target, decoded, 0o644))
		}
	}
	return dir
}

func (r *conformanceRunner) runCase(t *testing.T, c fixtureCase) runtimeObservation {
	t.Helper()
	dir := r.seedCaseWorkspace(t, c)
	projectID := uuid.NewString()
	testdbseed.InsertProjectRoot(t, r.h.DB, projectID, dir)
	sess, err := r.h.Store.Create(t.Context(), wire.CreateSessionRequest{ProjectID: projectID}, projectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "set implementer agent", r.h.Sessions.Manager.Chats.SetAgentType(t.Context(), sess.ID, "implementer"))
	_, err = r.h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "run case "+c.ID)
	testutil.FailErr(t, "prompt conformance case", err)

	callID := "call-" + c.ID
	msgs, err := r.h.Store.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read session messages", err)
	var content string
	for _, raw := range msgs {
		msg := messageview.RedactMessage(raw)
		if msg.Role != wire.MessageRoleTool || msg.ToolResult == nil || msg.ToolResult.ToolCallID != callID {
			continue
		}
		content = msg.ToolResult.Content
		break
	}
	if content == "" {
		t.Fatalf("case %s produced no tool output in %d messages", c.ID, len(msgs))
	}

	obs := runtimeObservation{
		Files:         snapshotTrackedFiles(dir, c.TrackedFiles),
		ChildReceipts: r.queryChildReceipts(t, sess.ID),
	}
	if strings.Contains(content, `"program_id"`) {
		idx := strings.Index(content, "{")
		dec := json.NewDecoder(strings.NewReader(content[idx:]))
		testutil.FailErr(t, "decode program outcome", dec.Decode(&obs.Outcome))
	} else {
		obs.TopLevelReject = content
	}
	return obs
}

func snapshotTrackedFiles(dir string, tracked []string) map[string]*string {
	out := make(map[string]*string, len(tracked))
	for _, rel := range tracked {
		target := filepath.Join(dir, filepath.FromSlash(rel))
		data, err := os.ReadFile(target)
		if err != nil {
			out[rel] = nil
			continue
		}
		text := string(data)
		out[rel] = &text
	}
	return out
}

func (r *conformanceRunner) queryChildReceipts(t *testing.T, sessionID string) []string {
	t.Helper()
	rows, err := r.h.DB.QueryContext(t.Context(), `SELECT e.operation_id, e.state FROM invocation_program_executions e
		JOIN invocation_programs p ON p.id = e.program_id JOIN invocation_receipts r ON r.id = p.parent_receipt_id
		WHERE r.session_id = ? AND e.operation_id NOT LIKE 'program %' ORDER BY e.ordinal`, sessionID)
	testutil.FailErr(t, "query child receipts", err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var op, state string
		testutil.FailErr(t, "scan child receipt", rows.Scan(&op, &state))
		out = append(out, op+"="+state)
	}
	testutil.FailErr(t, "iterate child receipts", rows.Err())
	return out
}
