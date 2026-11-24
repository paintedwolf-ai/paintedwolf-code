package wiring

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type replaySummarizer struct{}

func (replaySummarizer) Summarize(ctx context.Context, _, _ string, _ int) (string, error) {
	if compaction.SummaryFormatRequested(ctx) {
		return `{"facts":["Root policy verified","Parked command settled"],"completed":["Initial analysis"],"pending":["Final verification"],"reacquire":[],"constraints":["Run ./task"]}`, nil
	}
	return "Short chunk summary", nil
}

type replayLLMClient struct {
	mu       sync.Mutex
	requests []modelcall.CompletionRequest
	respond  func(idx int, req modelcall.CompletionRequest) (*modelcall.Completion, error)
}

func (c *replayLLMClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if compaction.SummaryFormatRequested(ctx) {
		return &modelcall.Completion{
			Content: `{"facts":["Root policy verified","Parked command settled"],"completed":["Initial analysis"],"pending":["Final verification"],"reacquire":[],"constraints":["Run ./task"]}`,
		}, nil
	}
	c.mu.Lock()
	idx := len(c.requests)
	c.requests = append(c.requests, req)
	fn := c.respond
	c.mu.Unlock()
	if fn != nil {
		return fn(idx, req)
	}
	return &modelcall.Completion{Content: "done"}, nil
}

func (c *replayLLMClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	comp, err := c.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{
		Content:    comp.Content,
		ToolCalls:  comp.ToolCalls,
		Done:       true,
		ProviderID: "mock",
		Model:      req.Model,
	}
	close(ch)
	return ch, nil
}

func (c *replayLLMClient) snapshot() []modelcall.CompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]modelcall.CompletionRequest(nil), c.requests...)
}

func anthropicPrefixThroughRootPolicy(t *testing.T, req modelcall.CompletionRequest, rootPolicyText, sessionID, model string) []byte {
	t.Helper()
	standingIdx, rootIdx, historyIdx := -1, -1, -1
	for i, m := range req.Messages {
		if m.PromptCacheBreakpoint == wire.PromptCacheTierStanding {
			standingIdx = i
		}
		if m.PromptCacheBreakpoint == wire.PromptCacheTierHistory {
			historyIdx = i
		}
		if strings.Contains(m.Content, rootPolicyText) {
			if rootIdx >= 0 {
				t.Fatalf("root policy appeared multiple times in request (at %d and %d)", rootIdx, i)
			}
			rootIdx = i
		}
	}
	if standingIdx < 0 || rootIdx < 0 {
		var summary []string
		for i, m := range req.Messages {
			c := m.Content
			if len(c) > 80 {
				c = c[:80]
			}
			summary = append(summary, fmt.Sprintf("[%d] role=%s origin=%s bp=%s id=%s content=%q", i, m.Role, m.Origin, m.PromptCacheBreakpoint, m.ID, c))
		}
		t.Fatalf("missing expected indices: standing=%d root=%d history=%d\n%s", standingIdx, rootIdx, historyIdx, strings.Join(summary, "\n"))
	}
	if !(standingIdx < rootIdx && (historyIdx < 0 || rootIdx < historyIdx)) {
		t.Fatalf("expected standing < root (< history if present), got standing=%d root=%d history=%d", standingIdx, rootIdx, historyIdx)
	}
	prefix := req.Messages[:rootIdx+1]
	sys, msgs := anthropic.ProjectMessages(prefix, nil, false, sessionID, "anthropic", model)
	payload, err := json.Marshal(struct {
		System   any `json:"system"`
		Messages any `json:"messages"`
	}{System: sys, Messages: msgs})
	testutil.FailErr(t, "marshal anthropic prefix through root policy", err)
	return payload
}

func TestRequestEfficiencyIntegratedReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	workspace, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "eval symlinks workspace", err)
	rootPolicyNeedle := "Root project guidance: always verify with ./task."
	rootPolicyText := rootPolicyNeedle + "\n" + strings.Repeat("Guideline detail. ", 24)
	nestedPolicyNeedle := "Nested sub guidance: check sub/work.txt before editing."
	nestedPolicyText := nestedPolicyNeedle
	testutil.FailErr(t, "write root AGENTS.md", os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte(rootPolicyText), 0o600))
	testutil.FailErr(t, "mkdir sub", os.MkdirAll(filepath.Join(workspace, "sub"), 0o755))
	testutil.FailErr(t, "write sub AGENTS.md", os.WriteFile(filepath.Join(workspace, "sub", "AGENTS.md"), []byte(nestedPolicyText), 0o600))
	testutil.FailErr(t, "write README.md", os.WriteFile(filepath.Join(workspace, "README.md"), []byte("root readme\n"), 0o600))
	testutil.FailErr(t, "write sub/work.txt", os.WriteFile(filepath.Join(workspace, "sub", "work.txt"), []byte("sub work\n"), 0o600))

	startedFile := filepath.Join(workspace, "cmd-started")
	gateFile := filepath.Join(workspace, "cmd-release")
	cmdScript := "printf ok > cmd-started; while [ ! -f cmd-release ]; do sleep 0.05; done; echo settled-output"

	client := &replayLLMClient{}
	var parkEntered = make(chan struct{})
	var parkOnce sync.Once
	client.respond = func(idx int, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
		switch idx {
		case 0:
			return &modelcall.Completion{
				ToolCalls: []wire.ToolCall{{
					ID:   "call-progress",
					Name: "update_progress",
					Args: map[string]any{"content": "## Progress\n- [ ] Inspect workspace\n- [ ] Run verification\n"},
				}},
			}, nil
		case 1:
			return &modelcall.Completion{
				ToolCalls: []wire.ToolCall{{ID: "call-read-1", Name: "command", Args: map[string]any{"command": "cat README.md"}}},
			}, nil
		case 2:
			return &modelcall.Completion{
				ToolCalls: []wire.ToolCall{{ID: "call-read-2", Name: "command", Args: map[string]any{"command": "cat sub/work.txt"}}},
			}, nil
		case 3:
			parkOnce.Do(func() { close(parkEntered) })
			return &modelcall.Completion{
				ToolCalls: []wire.ToolCall{{
					ID:   "call-cmd-park",
					Name: "command",
					Args: map[string]any{"command": cmdScript, "until_complete": true},
				}},
			}, nil
		default:
			return &modelcall.Completion{Content: "verified"}, nil
		}
	}

	storeDir := t.TempDir()
	h := BuildForTestAt(t, storeDir, WithLLMClient(client))
	h.Start(t)

	clock := programtest.NewManualClock()
	h.Commands.SetProgramClock(clock)

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{
		ProviderID: "mock",
		Model:      "mock",
		Posture:    wire.SessionPostureBuild,
	}, workspace)
	testutil.FailErr(t, "create harness session", err)

	// Run coordinator prompt through two tool exchanges and a parked command.
	type promptOut struct {
		err error
	}
	promptDone := make(chan promptOut, 1)
	go func() {
		_, perr := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "inspect files and run verification")
		promptDone <- promptOut{err: perr}
	}()

	select {
	case <-parkEntered:
	case <-ctx.Done():
		t.Fatal("timed out waiting for command dispatch")
	}
	for {
		if _, err := os.Stat(startedFile); err == nil {
			break
		}
		select {
		case out := <-promptDone:
			reqs := client.snapshot()
			var lastTool string
			if len(reqs) > 0 {
				for _, m := range reqs[len(reqs)-1].Messages {
					if m.Role == wire.MessageRoleTool {
						lastTool = m.Content
					}
				}
			}
			t.Fatalf("prompt exited before command started (err=%v, reqs=%d, lastTool=%q)", out.err, len(reqs), lastTool)
		case <-ctx.Done():
			t.Fatal("timed out waiting for command process to start")
		case <-time.After(20 * time.Millisecond):
		}
	}
	// Advance manual clock until the foreground wait budget expires and the turn parks.
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
waitPark:
	for {
		clock.Advance(toolcontract.ProgramInlineWait)
		select {
		case out := <-promptDone:
			testutil.FailErr(t, "parked prompt turn", out.err)
			break waitPark
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("timed out waiting for parked turn to yield")
		}
	}

	// Zero intermediate model dispatches between expiry and result.
	reqsBeforeSettle := client.snapshot()
	if len(reqsBeforeSettle) != 4 {
		t.Fatalf("model dispatches before command settlement = %d, want 4", len(reqsBeforeSettle))
	}

	// Verify RE03 pre-history root policy placement and identical prefix bytes through the policy block.
	prefix1 := anthropicPrefixThroughRootPolicy(t, reqsBeforeSettle[1], rootPolicyNeedle, sess.ID, sess.Model)
	prefix2 := anthropicPrefixThroughRootPolicy(t, reqsBeforeSettle[2], rootPolicyNeedle, sess.ID, sess.Model)
	if string(prefix1) != string(prefix2) {
		t.Fatalf("serialized prefix through root policy changed across same-scope tool exchanges")
	}
	// Verify root policy is projected as user (not privileged system) in Anthropic wire.
	sys1, msgs1 := anthropic.ProjectMessages(reqsBeforeSettle[1].Messages, nil, false, sess.ID, "anthropic", sess.Model)
	for _, blk := range sys1 {
		if strings.Contains(blk.Text, rootPolicyNeedle) {
			t.Fatal("root project policy was promoted to privileged system preamble")
		}
	}
	foundUserPolicy := false
	for _, m := range msgs1 {
		for _, blk := range m.Content {
			if m.Role == "user" && strings.Contains(blk.Text, rootPolicyNeedle) {
				foundUserPolicy = true
			}
		}
	}
	if !foundUserPolicy {
		t.Fatal("root project policy missing from user-role messages")
	}

	// Verify nested sub/AGENTS.md appears in tail after sub/work.txt read (request 3).
	req3TailJoined := ""
	historyIdx3 := -1
	for i, m := range reqsBeforeSettle[3].Messages {
		if m.PromptCacheBreakpoint == wire.PromptCacheTierHistory {
			historyIdx3 = i
			continue
		}
		if historyIdx3 >= 0 && i > historyIdx3 {
			req3TailJoined += m.Content + "\n"
		}
	}
	if !strings.Contains(req3TailJoined, nestedPolicyNeedle) {
		var dump []string
		for i, m := range reqsBeforeSettle[3].Messages {
			c := m.Content
			if len(c) > 80 {
				c = c[:80]
			}
			dump = append(dump, fmt.Sprintf("[%d] role=%s origin=%s bp=%s content=%q", i, m.Role, m.Origin, m.PromptCacheBreakpoint, c))
		}
		t.Fatalf("expected nested policy in tail after touching sub/work.txt\n%s", strings.Join(dump, "\n"))
	}

	// Release parked command and wait for terminal settlement + resume model call.
	testutil.FailErr(t, "write gate file", os.WriteFile(gateFile, []byte("ok\n"), 0o600))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(client.snapshot()) >= 5 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	reqsAfterSettle := client.snapshot()
	if len(reqsAfterSettle) != 5 {
		t.Fatalf("model dispatches after settlement = %d, want 5", len(reqsAfterSettle))
	}
	joinedResume := ""
	for _, m := range reqsAfterSettle[4].Messages {
		joinedResume += m.Content + "\n"
	}
	if !strings.Contains(joinedResume, "settled-output") {
		t.Fatalf("resumed model request missing settled command output")
	}

	// RE01: append enough ordinary assistant rows to exceed compaction trigger, verify continuation survival & reopen repair.
	var extra []wire.Message
	for i := 0; i < 60; i++ {
		extra = append(extra, wire.Message{
			ID:      fmt.Sprintf("replay-asst-%d", i),
			Role:    wire.MessageRoleAssistant,
			Content: fmt.Sprintf("Iteration %d analysis: %s", i, strings.Repeat("detailed trace output ", 400)),
		})
	}
	testutil.FailErr(t, "append compaction trigger messages", h.Store.Transcript.AppendMessages(h.OwnerCtx(t, ctx), sess.ID, extra...))

	histSvc := h.Sessions.Manager.Runner.History
	ownerCtx := h.OwnerCtx(t, ctx)
	var report compaction.CompactionReport
	for attempt := 0; attempt < 5; attempt++ {
		histSvc.Wait()
		report, err = histSvc.ForceCompact(ownerCtx, sess.ID)
		if err == nil || !strings.Contains(err.Error(), "compaction source changed") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	testutil.FailErr(t, "force compact", err)
	if !report.SessionCompacted {
		t.Fatalf("expected ForceCompact to compact session, got report=%+v", report)
	}
	histSvc.Wait()
	testutil.FailErr(t, "wait synthesis", histSvc.Runner.WaitContext(ownerCtx))

	view1, ok, err := h.Store.Compaction.GetCompactionView(ownerCtx, sess.ID)
	testutil.FailErr(t, "get compaction view", err)
	if !ok || view1.Continuation == nil {
		msgs, _ := h.Store.Transcript.GetMessages(ownerCtx, sess.ID)
		est := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(msgs))
		reloadedSess, _ := h.Store.Sessions.Get(ownerCtx, sess.ID)
		lcfg := histSvc.Limits.Compaction(ownerCtx, reloadedSess)
		t.Fatalf("expected continuation view after compaction, got ok=%v msgs=%d est=%d gen=%d lcfg=%+v", ok, len(msgs), est, reloadedSess.CompactionGeneration, lcfg)
	}
	hasContinuationPart := false
	for _, m := range view1.Messages {
		if len(m.ContentParts) == 1 && m.ContentParts[0].Source == "compaction_summary" {
			hasContinuationPart = true
			if m.CompactedChunk != nil {
				t.Fatalf("continuation row was generically rewritten: %+v", m.CompactedChunk)
			}
		}
	}
	if !hasContinuationPart {
		t.Fatal("expected typed compaction_summary row in compaction view")
	}

	// Second background pass must not rewrite the continuation row.
	genBefore := view1.Generation
	histSvc.Schedule(ownerCtx, sess)
	histSvc.Wait()
	testutil.FailErr(t, "wait second pass", histSvc.Runner.WaitContext(ownerCtx))
	view2, ok, err := h.Store.Compaction.GetCompactionView(h.OwnerCtx(t, ctx), sess.ID)
	testutil.FailErr(t, "get compaction view after second pass", err)
	if !ok || view2.Generation != genBefore {
		t.Fatalf("expected stable generation %d on second pass, got ok=%v gen=%d", genBefore, ok, view2.Generation)
	}

	// Verify post-compaction coordinator request sees root policy and surviving continuation.
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "confirm final state")
	testutil.FailErr(t, "post-compaction prompt", err)
	reqsPostCompact := client.snapshot()
	lastReq := reqsPostCompact[len(reqsPostCompact)-1]
	_ = anthropicPrefixThroughRootPolicy(t, lastReq, rootPolicyNeedle, sess.ID, sess.Model)
	joinedFinal := ""
	for _, m := range lastReq.Messages {
		joinedFinal += m.Content + "\n"
	}
	if !strings.Contains(joinedFinal, "Continuation context") {
		t.Fatalf("post-compaction request missing surviving continuation")
	}

	// Simulate damaged view (continuation rewritten to compacted_chunk) and verify reopen repair.
	histSvc.Wait()
	latestView, ok, err := h.Store.Compaction.GetCompactionView(ownerCtx, sess.ID)
	testutil.FailErr(t, "reload compaction view", err)
	if !ok {
		t.Fatal("expected compaction view before simulating damage")
	}
	damaged := *latestView
	damaged.Generation = latestView.Generation + 1
	for i := range damaged.Messages {
		if len(damaged.Messages[i].ContentParts) == 1 && damaged.Messages[i].ContentParts[0].Source == "compaction_summary" {
			damaged.Messages[i].ContentParts[0].Source = "compacted_chunk"
			damaged.Messages[i].CompactedChunk = &wire.CompactedChunkMeta{OriginalTokens: 2000, CompactedTokens: 900}
		}
	}
	testutil.FailErr(t, "write damaged view", h.Store.Compaction.PutCompactionView(ownerCtx, sess.ID, damaged))
	testutil.FailErr(t, "close first host", h.Close())

	reopened := BuildForTestAt(t, storeDir, WithLLMClient(client))
	reopened.Start(t)
	_, ok, err = reopened.Store.Compaction.GetCompactionView(reopened.OwnerCtx(t, ctx), sess.ID)
	testutil.FailErr(t, "get damaged view on reopen", err)
	if ok {
		t.Fatal("expected damaged compaction view to be rejected for canonical rebuild")
	}
}
