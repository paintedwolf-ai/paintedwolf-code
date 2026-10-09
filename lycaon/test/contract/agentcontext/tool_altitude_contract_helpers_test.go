package contract

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func altitudeBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	return sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{
		{
			ID:    toolprofiles.DefaultToolProfileID,
			Tools: map[string]bool{"read": true, "grep": true, "find": true, "list_dir": true},
		},
	})
}

func altitudeCtx(dir, sessionID string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	ctx := tools.ToolContext{Roots: roots, ActiveRootID: "r1", Agent: toolprofiles.DefaultToolProfileID}
	ctx.SessionID = sessionID
	// Publish a below-threshold count for open-root altitude tests.
	ctx.RepoFileCount = 100
	ctx.RepoFileCountKnown = true
	return ctx
}

func writeLargeGoFile(t *testing.T, dir, name string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("package big\n\nfunc Marker() {}\n")
	for i := 0; i < readcaps.AutoOutlineThreshold+10; i++ {
		b.WriteString("// filler\n")
	}
	contractcheck.FailErr(t, "write large file", os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644))
}

func writeGrepHits(t *testing.T, dir string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("needle\n")
	}
	contractcheck.FailErr(t, "write grep hits", os.WriteFile(filepath.Join(dir, "many.txt"), []byte(b.String()), 0o644))
}

func writeManyFindFiles(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("file_%04d.txt", i)
		contractcheck.FailErr(t, "write find file", os.WriteFile(filepath.Join(dir, name), []byte("file "+name), 0o644))
	}
}

type stubCuratorProvider struct {
	id        string
	responses []string
	calls     int
	err       error
}

func (s *stubCuratorProvider) ID() string { return s.id }

func (s *stubCuratorProvider) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if s.err != nil {
		return nil, s.err
	}
	i := s.calls
	s.calls++
	if len(s.responses) == 0 {
		return &modelcall.Completion{Content: `{}`}, nil
	}
	if i >= len(s.responses) {
		i = len(s.responses) - 1
	}
	return &modelcall.Completion{Content: s.responses[i]}, nil
}

func (s *stubCuratorProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 1)
	go func() {
		defer close(ch)
		c, err := s.Complete(ctx, req)
		if err != nil {
			return
		}
		ch <- modelcall.StreamChunk{Content: c.Content, Done: true}
	}()
	return ch, nil
}

func (s *stubCuratorProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "lite"}}
}

func (s *stubCuratorProvider) Profile() providerprofile.Profile { return providerprofile.Profile{} }

func snapshotFromReadLine(t *testing.T, path, lineContent string, line int) evidence.Ledger {
	t.Helper()
	readJSON := fmt.Sprintf(`{"path":%q,"content":%q,"offset":%d,"end_line":%d,"limit":1}`,
		path, lineContent, line, line)
	return ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": path, "offset": line, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})
}

func assertNoCuratedFields(t *testing.T, raw string) {
	t.Helper()
	for _, key := range []string{"highlights", "gloss", "selected", "total", "view", "distribution"} {
		if strings.Contains(raw, `"`+key+`"`) {
			t.Fatalf("literal response must not include curated field %q: %s", key, raw)
		}
	}
}

func assertSurveyResponseHasCoverage(t *testing.T, raw string) {
	t.Helper()
	if !strings.Contains(raw, `"selected"`) || !strings.Contains(raw, `"total"`) {
		t.Fatalf("survey response missing selected/total: %s", raw)
	}
}
