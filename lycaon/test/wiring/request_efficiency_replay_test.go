package wiring

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type replayLLMClient struct {
	mu       sync.Mutex
	requests []modelcall.CompletionRequest
	respond  func(idx int, req modelcall.CompletionRequest) (*modelcall.Completion, error)
}

func (c *replayLLMClient) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
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

type countingReplaySummarizer struct {
	synthCalls atomic.Int32
	chunkCalls atomic.Int32
}

func (s *countingReplaySummarizer) Summarize(ctx context.Context, _, _ string, _ int) (string, error) {
	if compaction.SummaryFormatRequested(ctx) {
		s.synthCalls.Add(1)
		payload := map[string][]string{
			"facts": {
				"Root policy verified: repository guidance remains in user-role context before history across turns.",
				"Workspace layout inspected across root README, docs/notes.md, and nested sub/work.txt module.",
				"Compaction view preserves typed continuation identity across generic background chunk passes.",
				"Command completion disposition parks long-running verification work without polling dispatches.",
				"Evidence handles remain addressable for exact record recall after transcript prefix compaction.",
				"Nested sub/AGENTS.md remains scoped to tail delivery when sub-directory paths enter active turn.",
				"Session checklist state tracks pending verification across parked command settlement boundaries.",
				"Canonical invocation and command program receipts remain persisted in SQLite session storage.",
			},
			"completed": {
				"Read root README.md and verified top-level workspace structure and package boundaries.",
				"Read docs/notes.md in the same root policy scope with byte-identical prompt prefix reuse.",
				"Read sub/work.txt and confirmed scoped admission of nested sub/AGENTS.md policy text.",
				"Recorded 64 module inspection receipts and program completion records in session transcript.",
				"Verified idle prefix preparation matches live completion assembly when root policy is active.",
			},
			"pending": {
				"Execute slow verification script in sub with explicit until_complete disposition.",
				"Confirm zero intermediate model dispatches while verification command waits past inline budget.",
				"Inspect settled command output together with applicable policy and surviving continuation.",
				"Close out verification progress checklist item once terminal command output is observed.",
				"Verify reopen reuse of valid enriched continuation view and recovery from damaged view state.",
			},
			"reacquire": {
				"command#25: recall(query=\"handle:command#25\") for module 15 inspection output details.",
				"command#30: recall(query=\"handle:command#30\") for module 20 boundary verification notes.",
				"command#35: recall(query=\"handle:command#35\") for module 25 interface check output.",
				"command#40: recall(query=\"handle:command#40\") for module 30 dependency graph snapshot.",
				"command#45: recall(query=\"handle:command#45\") for module 35 schema validation output.",
				"command#50: recall(query=\"handle:command#50\") for module 40 wire serialization check.",
				"command#55: recall(query=\"handle:command#55\") for module 45 store migration verification.",
				"command#60: recall(query=\"handle:command#60\") for module 50 session recovery verification.",
			},
			"constraints": {
				"Always run verification through ./task rather than raw tool invocations.",
				"Keep repository guidance in non-system message roles so authority boundaries hold.",
				"Preserve exact evidence handles and receipt identifiers across compaction cycles.",
				"Update checklist items to completed before emitting final build-posture summary.",
				"Avoid polling background processes when explicit completion disposition is available.",
			},
		}
		raw, err := json.Marshal(payload)
		return string(raw), err
	}
	s.chunkCalls.Add(1)
	return "Short chunk summary", nil
}

type serializedAnthropicRequest struct {
	FullJSON           []byte
	SystemJSON         []byte
	Breakpoints        []providerwire.PromptCacheBreakpoint
	StandingHasMarker  bool
	HistoryHasMarker   bool
	TopLevelHasMarker  bool
	StandingMarkerByte int
	HistoryMarkerByte  int
}

func serializeFullAnthropicRequest(t *testing.T, req modelcall.CompletionRequest, sessionID string) serializedAnthropicRequest {
	t.Helper()
	policy := providerprofile.PromptCachePolicy{
		Profile: "anthropic",
		Mode:    providerprofile.PromptCacheExplicitBreakpoints,
		Lifetime: providerprofile.PromptCacheLifetime{
			Standing: providerprofile.CacheDuration(time.Hour),
			History:  providerprofile.CacheDuration(5 * time.Minute),
		},
	}
	provider := anthropic.New("anthropic", "https://api.anthropic.com/v1", "test-key", []modelinfo.Entry{
		{ID: "claude-sonnet-4-5"},
	}).WithPromptCache(policy)

	projReq := req
	projReq.Model = "claude-sonnet-4-5"
	projReq.Debug.SessionID = sessionID

	proj := providerwire.ProjectPromptCache(projReq, policy, modelinfo.ModelCapabilities{})
	breakpoints := providerwire.TrailingPromptCacheBreakpoints(proj.Breakpoints, providerwire.AnthropicExplicitBreakpointLimit)
	wireReq := provider.Prepare(projReq, false)

	standingMarked := len(wireReq.System) > 0 &&
		wireReq.System[len(wireReq.System)-1].CacheControl != nil &&
		wireReq.System[len(wireReq.System)-1].CacheControl.TTL == "1h"
	historyMarked := false
	for _, m := range wireReq.Messages {
		for _, blk := range m.Content {
			if blk.CacheControl != nil && blk.CacheControl.TTL == "5m" {
				historyMarked = true
			}
		}
	}
	topMarked := wireReq.CacheControl != nil && wireReq.CacheControl.TTL == "5m"

	sysRaw, err := surveyjson.Marshal(wireReq.System)
	testutil.FailErr(t, "marshal anthropic system", err)
	fullRaw, err := surveyjson.Marshal(wireReq)
	testutil.FailErr(t, "marshal full anthropic request", err)

	return serializedAnthropicRequest{
		FullJSON:           fullRaw,
		SystemJSON:         sysRaw,
		Breakpoints:        breakpoints,
		StandingHasMarker:  standingMarked,
		HistoryHasMarker:   historyMarked,
		TopLevelHasMarker:  topMarked,
		StandingMarkerByte: bytes.Index(fullRaw, []byte(`"cache_control":{"type":"ephemeral","ttl":"1h"}`)),
		HistoryMarkerByte:  bytes.Index(fullRaw, []byte(`"cache_control":{"type":"ephemeral","ttl":"5m"}`)),
	}
}

func commonPrefixLen(a, b []byte) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func forceCompactSettled(t *testing.T, ctx context.Context, h *Harness, sessionID string) compaction.CompactionReport {
	t.Helper()
	histSvc := h.Sessions.Manager.Runner.History
	ownerCtx := h.OwnerCtx(t, ctx)
	var report compaction.CompactionReport
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		histSvc.Wait()
		report, err = histSvc.ForceCompact(ownerCtx, sessionID)
		if err == nil || !strings.Contains(err.Error(), "compaction source changed") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	testutil.FailErr(t, "force compact", err)
	histSvc.Wait()
	testutil.FailErr(t, "wait synthesis", histSvc.Runner.WaitContext(ownerCtx))
	return report
}

func TestRequestEfficiencyIntegratedReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	workspace, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "eval symlinks workspace", err)

	rootPolicyNeedle := "Root project guidance: always verify with ./task."
	rootPolicyText := rootPolicyNeedle + "\n" + strings.Repeat("Guideline detail for repository operations. ", 32)
	nestedPolicyNeedle := "Nested sub guidance: check sub/work.txt before editing."

	testutil.FailErr(t, "write root AGENTS.md", os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte(rootPolicyText), 0o600))
	testutil.FailErr(t, "mkdir docs", os.MkdirAll(filepath.Join(workspace, "docs"), 0o755))
	testutil.FailErr(t, "mkdir sub", os.MkdirAll(filepath.Join(workspace, "sub"), 0o755))
	testutil.FailErr(t, "write sub AGENTS.md", os.WriteFile(filepath.Join(workspace, "sub", "AGENTS.md"), []byte(nestedPolicyNeedle), 0o600))
	testutil.FailErr(t, "write README.md", os.WriteFile(filepath.Join(workspace, "README.md"), []byte("root readme\n"), 0o600))
	testutil.FailErr(t, "write docs/notes.md", os.WriteFile(filepath.Join(workspace, "docs", "notes.md"), []byte("root notes\n"), 0o600))
	testutil.FailErr(t, "write sub/work.txt", os.WriteFile(filepath.Join(workspace, "sub", "work.txt"), []byte("sub work\n"), 0o600))

	startedFile := filepath.Join(workspace, "cmd-started")
	gateFile := filepath.Join(workspace, "cmd-release")
	cmdScript := "cat sub/work.txt >/dev/null; printf ok > cmd-started; while [ ! -f cmd-release ]; do sleep 0.05; done; echo settled-verification-pass"

	client := &replayLLMClient{}
	parkDispatched := make(chan struct{})
	var parkOnce sync.Once
	client.respond = func(idx int, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
		switch idx {
		case 0:
			return &modelcall.Completion{
				ToolCalls: []wire.ToolCall{{ID: "call-read-1", Name: "command", Args: map[string]any{"command": "cat README.md"}}},
			}, nil
		case 1:
			return &modelcall.Completion{
				ToolCalls: []wire.ToolCall{{ID: "call-read-2", Name: "command", Args: map[string]any{"command": "cat docs/notes.md"}}},
			}, nil
		case 2:
			return &modelcall.Completion{
				ToolCalls: []wire.ToolCall{{ID: "call-read-sub", Name: "command", Args: map[string]any{"command": "cat sub/work.txt"}}},
			}, nil
		case 3:
			return &modelcall.Completion{Content: "inspection complete"}, nil
		case 4:
			return &modelcall.Completion{
				ToolCalls: []wire.ToolCall{{
					ID:   "call-progress",
					Name: "update_progress",
					Args: map[string]any{"content": "## Progress\n- [ ] Run verification\n"},
				}},
			}, nil
		case 5:
			parkOnce.Do(func() { close(parkDispatched) })
			return &modelcall.Completion{
				ToolCalls: []wire.ToolCall{{
					ID:   "call-cmd-park",
					Name: "command",
					Args: map[string]any{"command": cmdScript, "until_complete": true},
				}},
			}, nil
		case 6:
			return &modelcall.Completion{
				ToolCalls: []wire.ToolCall{{
					ID:   "call-progress-done",
					Name: "update_progress",
					Args: map[string]any{"content": "## Progress\n- [x] Run verification\n", "expected_revision": 1},
				}},
			}, nil
		default:
			return &modelcall.Completion{Content: "all verified"}, nil
		}
	}

	storeDir := t.TempDir()
	h := BuildForTestAt(t, storeDir, WithLLMClient(client))
	h.Start(t)

	clock := programtest.NewManualClock()
	h.Commands.SetProgramClock(clock)

	synth := &countingReplaySummarizer{}
	histSvc := h.Sessions.Manager.Runner.History
	histSvc.SetCompactor(compaction.NewSimpleCompactor(compaction.DefaultCompactionConfig(), synth))

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{
		ProviderID: "mock",
		Model:      "mock",
		Posture:    wire.SessionPostureBuild,
	}, workspace)
	testutil.FailErr(t, "create harness session", err)
	ownerCtx := h.OwnerCtx(t, ctx)

	// Phase 1: Two root-scope tool exchanges + one sub-scope tool exchange.
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "inspect workspace and prepare verification")
	testutil.FailErr(t, "turn 1 prompt", err)

	reqsTurn1 := client.snapshot()
	if len(reqsTurn1) != 4 {
		t.Fatalf("turn 1 model dispatches = %d, want 4", len(reqsTurn1))
	}

	// Full Anthropic wire serialization with production cache breakpoints on
	// two same-scope appends (reqs[1] vs reqs[2]).
	ser2 := serializeFullAnthropicRequest(t, reqsTurn1[1], sess.ID)
	ser3 := serializeFullAnthropicRequest(t, reqsTurn1[2], sess.ID)
	if !ser2.StandingHasMarker || !ser2.HistoryHasMarker || !ser2.TopLevelHasMarker ||
		!ser3.StandingHasMarker || !ser3.HistoryHasMarker || !ser3.TopLevelHasMarker {
		t.Fatalf("expected standing, history, and top-level cache markers on both requests: ser2=(%v,%v,%v) ser3=(%v,%v,%v)",
			ser2.StandingHasMarker, ser2.HistoryHasMarker, ser2.TopLevelHasMarker,
			ser3.StandingHasMarker, ser3.HistoryHasMarker, ser3.TopLevelHasMarker)
	}
	if bytes.Contains(ser2.SystemJSON, []byte(rootPolicyNeedle)) {
		t.Fatal("root project policy was promoted to privileged system preamble")
	}
	if got := bytes.Count(ser2.FullJSON, []byte(rootPolicyNeedle)); got != 1 {
		t.Fatalf("root policy occurrences in request 1 = %d, want 1", got)
	}
	if got := bytes.Count(ser3.FullJSON, []byte(rootPolicyNeedle)); got != 1 {
		t.Fatalf("root policy occurrences in request 2 = %d, want 1", got)
	}
	rootStart := bytes.Index(ser2.FullJSON, []byte(rootPolicyNeedle))
	rootEnd := rootStart + len(rootPolicyText)
	sharedPrefixBytes := commonPrefixLen(ser2.FullJSON, ser3.FullJSON)
	if rootStart < 0 || sharedPrefixBytes <= rootEnd {
		t.Fatalf("common serialized prefix (%d bytes) did not cover root policy [%d..%d]", sharedPrefixBytes, rootStart, rootEnd)
	}

	// Sub-scope exchange (reqs[3], after cat sub/work.txt) keeps root policy before history and adds sub/AGENTS.md in tail.
	ser4 := serializeFullAnthropicRequest(t, reqsTurn1[3], sess.ID)
	if !bytes.Contains(ser4.FullJSON, []byte(nestedPolicyNeedle)) {
		t.Fatal("expected nested sub/AGENTS.md in request 3 after touching sub/work.txt")
	}
	manifest4 := h.Sessions.Manager.Coordinator.PolicyIndex.ManifestFor(reqsTurn1[3].Messages)
	if len(manifest4.Files) != 2 {
		t.Fatalf("policy manifest files on sub-scope request = %d, want 2 (root + sub)", len(manifest4.Files))
	}

	// Verify idle BuildKnownPrefix matches BuildCompletionMessages with root policy enabled.
	histSvc.Wait()
	reloadedSess, err := h.Store.Sessions.Get(ownerCtx, sess.ID)
	testutil.FailErr(t, "reload idle session", err)
	coord := h.Sessions.Manager.Coordinator
	frame, err := coord.Assembly.Frame.BuildCoordinatorTurnFrame(ownerCtx, sess.ID, reloadedSess)
	testutil.FailErr(t, "build coordinator frame", err)
	profileID, err := coord.Profiles.PromptToolProfile(ownerCtx, reloadedSess)
	testutil.FailErr(t, "resolve prompt tool profile", err)
	frame.Machine = coord.Profiles.ProjectMachine(ownerCtx, reloadedSess, profileID)
	if coord.Assembly.ToolContext != nil {
		frame.Machine.Surface.SessionScratchDir = coord.Assembly.ToolContext.ScratchDir(ownerCtx, reloadedSess)
		frame.Machine.Surface.Fingerprint = frame.Machine.Surface.CacheFingerprint()
	}
	prepCtx := toolpolicy.WithCoordinatorTurnFrame(ownerCtx, &frame)
	projectedRows, err := histSvc.Project(prepCtx, reloadedSess)
	testutil.FailErr(t, "project idle history", err)
	asm := h.CoordinatorRuntime.Assembly()
	knownMsgs, err := asm.BuildKnownPrefix(prepCtx, reloadedSess, projectedRows, frame)
	testutil.FailErr(t, "BuildKnownPrefix", err)
	asm.BeginPromptTurn(sess.ID)
	fullMsgs, err := asm.BuildCompletionMessages(prepCtx, reloadedSess, projectedRows, &frame)
	asm.EndPromptTurn(sess.ID)
	testutil.FailErr(t, "BuildCompletionMessages", err)
	if len(knownMsgs) < 4 || len(fullMsgs) <= len(knownMsgs) {
		t.Fatalf("unexpected prefix lengths: known=%d full=%d", len(knownMsgs), len(fullMsgs))
	}
	for i := range knownMsgs {
		if knownMsgs[i].Role != fullMsgs[i].Role ||
			knownMsgs[i].Content != fullMsgs[i].Content ||
			knownMsgs[i].PromptCacheBreakpoint != fullMsgs[i].PromptCacheBreakpoint ||
			!reflect.DeepEqual(knownMsgs[i].ToolCalls, fullMsgs[i].ToolCalls) {
			t.Fatalf("idle BuildKnownPrefix diverged from live completion at %d:\nknown=%+v\nfull=%+v", i, knownMsgs[i], fullMsgs[i])
		}
	}

	// Phase 2: Seed long history with tool receipts/handles so continuation exceeds 1,800-token chunk threshold,
	// run full compaction + local synthesis enrichment, then exercise generic chunk passes.
	var longHistory []wire.Message
	exitZero := 0
	for i := 1; i <= 64; i++ {
		callID := fmt.Sprintf("seed-call-%d", i)
		handles := []string{
			fmt.Sprintf("command#module-subsystem-boundary-verification-%d", 10+i),
			fmt.Sprintf("read#module-subsystem-boundary-verification-%d", 10+i),
		}
		if i == 15 {
			handles[0] = "command#25"
		}
		cmdText := fmt.Sprintf("cat pkg/subsystem_%02d/boundary_contract_%02d.go && stat pkg/subsystem_%02d/manifest_verification_%02d.go && wc -l pkg/subsystem_%02d/projection_dispatch_%02d.go", i, i, i, i, i, i)
		longHistory = append(longHistory,
			wire.Message{
				ID:        fmt.Sprintf("seed-asst-%d", i),
				Role:      wire.MessageRoleAssistant,
				Content:   fmt.Sprintf("Step %d inspection notes: %s", i, strings.Repeat("verified module boundary ", 160)),
				ToolCalls: []wire.ToolCall{{ID: callID, Name: "command", Args: map[string]any{"command": cmdText}}},
			},
			wire.Message{
				ID:              fmt.Sprintf("seed-tool-%d", i),
				Role:            wire.MessageRoleTool,
				EvidenceHandles: handles,
				ToolResult: &wire.ToolResult{
					ToolCallID:     callID,
					Tool:           "command",
					Outcome:        wire.ToolResultOutcomeCompleted,
					Content:        fmt.Sprintf("File %d body: %s", i, strings.Repeat("package detail ", 240)),
					Invocation:     &wire.InvocationReceipt{ID: fmt.Sprintf("seed-rcpt-%d", i), ToolCallID: callID, Tool: "command", Invoked: true, Status: "completed", SourceVerdict: "passed"},
					CommandProgram: &wire.CommandProgramReceipt{ID: fmt.Sprintf("seed-prog-%d", i), ParentReceiptID: fmt.Sprintf("seed-rcpt-%d", i), State: "completed", StatusCode: &exitZero},
				},
			},
		)
	}
	testutil.FailErr(t, "append long history", h.Store.Transcript.AppendMessages(ownerCtx, sess.ID, longHistory...))

	compactReport := forceCompactSettled(t, ctx, h, sess.ID)
	if !compactReport.SessionCompacted {
		t.Fatalf("expected full session compaction, got %+v", compactReport)
	}

	view1, ok, err := h.Store.Compaction.GetCompactionView(ownerCtx, sess.ID)
	testutil.FailErr(t, "get compaction view after enrichment", err)
	if !ok || view1.Continuation == nil {
		t.Fatalf("expected enriched continuation view, got ok=%v", ok)
	}
	if view1.Continuation.SummaryThroughOrd <= 0 || len(view1.Continuation.Summary.Facts) == 0 {
		t.Fatalf("expected enrichment to advance SummaryThroughOrd with accepted facts, got %+v", view1.Continuation)
	}
	enrichmentPublishes := view1.Generation - compactReport.CompactionGeneration
	if enrichmentPublishes != 1 {
		t.Fatalf("enrichment publications = %d, want 1 (baseline gen=%d, view gen=%d)", enrichmentPublishes, compactReport.CompactionGeneration, view1.Generation)
	}
	synthCountAfterEnrich := synth.synthCalls.Load()
	if synthCountAfterEnrich <= 0 {
		t.Fatalf("expected synthesis calls > 0, got %d", synthCountAfterEnrich)
	}

	continuationTokens := 0
	for _, m := range view1.Messages {
		if len(m.ContentParts) == 1 && m.ContentParts[0].Source == "compaction_summary" {
			continuationTokens = tokenest.EstimateDefault(m.Content)
			if m.CompactedChunk != nil {
				t.Fatalf("continuation was rewritten during compaction: %+v", m.CompactedChunk)
			}
		}
	}
	if continuationTokens < compaction.DefaultCompactionConfig().ChunkTokenThreshold {
		t.Fatalf("expected oversized continuation >= %d tokens, got %d", compaction.DefaultCompactionConfig().ChunkTokenThreshold, continuationTokens)
	}

	// Append an oversized ordinary assistant row quoting "## Continuation" and
	// age it past KeepRecentMessages.
	oversizedOrdinaryID := "ordinary-oversized-asst"
	var postCompactRows []wire.Message
	postCompactRows = append(postCompactRows, wire.Message{
		ID:      oversizedOrdinaryID,
		Role:    wire.MessageRoleAssistant,
		Content: "## Continuation\nQuoted heading in ordinary assistant analysis: " + strings.Repeat("ordinary long explanation ", 380),
	})
	for i := 0; i < 18; i++ {
		postCompactRows = append(postCompactRows, wire.Message{
			ID:      fmt.Sprintf("tail-note-%d", i),
			Role:    wire.MessageRoleAssistant,
			Content: fmt.Sprintf("short tail note %d", i),
		})
	}
	testutil.FailErr(t, "append post-compact rows", h.Store.Transcript.AppendMessages(ownerCtx, sess.ID, postCompactRows...))

	// Background pass 1: ordinary oversized assistant row shrinks; oversized typed continuation survives untouched.
	histSvc.Schedule(ownerCtx, sess)
	histSvc.Wait()
	testutil.FailErr(t, "wait chunk pass 1", histSvc.Runner.WaitContext(ownerCtx))

	view2, ok, err := h.Store.Compaction.GetCompactionView(ownerCtx, sess.ID)
	testutil.FailErr(t, "get view after chunk pass 1", err)
	if !ok {
		t.Fatal("expected compaction view after chunk pass 1")
	}
	ordinaryShrank, continuationSurvived := false, false
	for _, m := range view2.Messages {
		if m.ID == oversizedOrdinaryID && m.CompactedChunk != nil && m.CompactedChunk.CompactedTokens < m.CompactedChunk.OriginalTokens {
			ordinaryShrank = true
		}
		if len(m.ContentParts) == 1 && m.ContentParts[0].Source == "compaction_summary" && m.CompactedChunk == nil {
			continuationSurvived = true
		}
	}
	if !ordinaryShrank || !continuationSurvived {
		t.Fatalf("chunk pass 1: ordinaryShrank=%v continuationSurvived=%v", ordinaryShrank, continuationSurvived)
	}

	// Background pass 2 (only the oversized continuation remains above threshold): zero generic replacements.
	genBeforePass2 := view2.Generation
	histSvc.Schedule(ownerCtx, sess)
	histSvc.Wait()
	testutil.FailErr(t, "wait chunk pass 2", histSvc.Runner.WaitContext(ownerCtx))

	view3, ok, err := h.Store.Compaction.GetCompactionView(ownerCtx, sess.ID)
	testutil.FailErr(t, "get view after chunk pass 2", err)
	if !ok || view3.Generation != genBeforePass2 {
		t.Fatalf("expected zero generic replacements on pass 2 (gen=%d), got ok=%v gen=%d", genBeforePass2, ok, view3.Generation)
	}
	if got := synth.synthCalls.Load(); got != synthCountAfterEnrich {
		t.Fatalf("unexpected synthesis calls during generic chunk passes: got=%d want=%d", got, synthCountAfterEnrich)
	}

	// Phase 3: Dispatch slow verification command with until_complete:true AFTER compaction,
	// park on foreground expiry, settle, and inspect the single resumed request for all three invariants together.
	promptDone := make(chan error, 1)
	go func() {
		_, perr := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "run slow verification command in sub")
		promptDone <- perr
	}()

	select {
	case <-parkDispatched:
	case <-ctx.Done():
		t.Fatal("timed out waiting for turn 2 command dispatch")
	}
	for {
		if _, statErr := os.Stat(startedFile); statErr == nil {
			break
		}
		select {
		case perr := <-promptDone:
			t.Fatalf("turn 2 exited before command started: err=%v", perr)
		case <-ctx.Done():
			t.Fatal("timed out waiting for command process to start")
		case <-time.After(20 * time.Millisecond):
		}
	}

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
waitPark:
	for {
		clock.Advance(toolcontract.ProgramInlineWait)
		select {
		case perr := <-promptDone:
			testutil.FailErr(t, "parked turn 2", perr)
			break waitPark
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("timed out waiting for turn 2 to park")
		}
	}

	// Zero intermediate model dispatches between foreground expiry and command settlement.
	reqsWhileParked := client.snapshot()
	if len(reqsWhileParked) != 6 {
		t.Fatalf("model dispatches while parked = %d, want 6 (zero intermediate dispatches)", len(reqsWhileParked))
	}

	// Release gate file so the parked command settles and resumes the session.
	testutil.FailErr(t, "write gate file", os.WriteFile(gateFile, []byte("ok\n"), 0o600))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(client.snapshot()) >= 8 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	reqsAfterResume := client.snapshot()
	if len(reqsAfterResume) != 8 {
		t.Fatalf("model dispatches after settlement = %d, want 8", len(reqsAfterResume))
	}

	// Wait for resumed turn to settle into Idle before inspecting durable state.
	for time.Now().Before(deadline) {
		cur, gerr := h.Store.Sessions.Get(ownerCtx, sess.ID)
		if gerr == nil && cur.Status == wire.SessionStatusIdle {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	// Assert the single resumed request (reqsAfterResume[6]) carries ALL THREE together:
	// 1. Settled command terminal report ("settled-verification-pass").
	// 2. Applicable policy (root AGENTS.md in pre-history user-role prefix + sub/AGENTS.md in tail + exact manifest).
	// 3. Surviving enriched continuation ("Root policy verified" + compacted source handle) and progress checklist.
	resumedReq := reqsAfterResume[6]
	serResumed := serializeFullAnthropicRequest(t, resumedReq, sess.ID)
	if !serResumed.StandingHasMarker || !serResumed.HistoryHasMarker || !serResumed.TopLevelHasMarker {
		t.Fatalf("resumed request missing cache markers: standing=%v history=%v top=%v",
			serResumed.StandingHasMarker, serResumed.HistoryHasMarker, serResumed.TopLevelHasMarker)
	}
	if bytes.Contains(serResumed.SystemJSON, []byte(rootPolicyNeedle)) {
		t.Fatal("resumed request promoted root policy to system")
	}
	if got := bytes.Count(serResumed.FullJSON, []byte(rootPolicyNeedle)); got != 1 {
		t.Fatalf("resumed request root policy count = %d, want 1", got)
	}
	if !bytes.Contains(serResumed.FullJSON, []byte(nestedPolicyNeedle)) {
		t.Fatal("resumed request missing sub/AGENTS.md after command read sub/work.txt")
	}
	resumedManifest := h.Sessions.Manager.Coordinator.PolicyIndex.ManifestFor(resumedReq.Messages)
	if len(resumedManifest.Files) != 2 {
		t.Fatalf("resumed request policy manifest files = %d, want 2", len(resumedManifest.Files))
	}

	var joinedResumed strings.Builder
	for _, m := range resumedReq.Messages {
		joinedResumed.WriteString(m.Content)
		joinedResumed.WriteByte('\n')
	}
	resumedText := joinedResumed.String()
	for _, want := range []string{
		"settled-verification-pass",
		"Root policy verified",
		"command#25",
		"Run verification",
	} {
		if !strings.Contains(resumedText, want) {
			t.Fatalf("resumed request missing %q", want)
		}
	}

	// Phase 4: Reopen valid view (reused), then simulate damaged view and reopen twice (rebuilt once, then reused).
	histSvc.Wait()
	validBeforeClose, ok, err := h.Store.Compaction.GetCompactionView(ownerCtx, sess.ID)
	testutil.FailErr(t, "read valid view before close", err)
	if !ok || validBeforeClose.Continuation == nil {
		t.Fatal("expected valid continuation view before close")
	}
	testutil.FailErr(t, "close initial host", h.Close())

	reopen1 := BuildForTestAt(t, storeDir, WithLLMClient(client))
	reopen1.Start(t)
	reopen1Ctx := reopen1.OwnerCtx(t, ctx)
	reusedView, ok, err := reopen1.Store.Compaction.GetCompactionView(reopen1Ctx, sess.ID)
	testutil.FailErr(t, "read view on reopen 1", err)
	if !ok || reusedView.Generation != validBeforeClose.Generation || reusedView.Continuation == nil || reusedView.Continuation.SummaryThroughOrd <= 0 {
		t.Fatalf("reopen 1 did not reuse valid enriched view: ok=%v view=%+v", ok, reusedView)
	}

	damaged := *reusedView
	damaged.Generation = reusedView.Generation + 1
	for i := range damaged.Messages {
		if len(damaged.Messages[i].ContentParts) == 1 && damaged.Messages[i].ContentParts[0].Source == "compaction_summary" {
			damaged.Messages[i].ContentParts[0].Source = "compacted_chunk"
			damaged.Messages[i].CompactedChunk = &wire.CompactedChunkMeta{OriginalTokens: continuationTokens, CompactedTokens: 900}
		}
	}
	testutil.FailErr(t, "write damaged view", reopen1.Store.Compaction.PutCompactionView(reopen1Ctx, sess.ID, damaged))
	testutil.FailErr(t, "close reopen 1", reopen1.Close())

	reopen2 := BuildForTestAt(t, storeDir, WithLLMClient(client))
	reopen2.Start(t)
	reopen2Ctx := reopen2.OwnerCtx(t, ctx)
	if _, ok, err = reopen2.Store.Compaction.GetCompactionView(reopen2Ctx, sess.ID); err != nil || ok {
		t.Fatalf("expected damaged view to be rejected on reopen 2, got ok=%v err=%v", ok, err)
	}
	reopen2.Sessions.Manager.Runner.History.SetCompactor(compaction.NewSimpleCompactor(compaction.DefaultCompactionConfig(), synth))
	_ = forceCompactSettled(t, ctx, reopen2, sess.ID)
	repairedView, ok, err := reopen2.Store.Compaction.GetCompactionView(reopen2Ctx, sess.ID)
	testutil.FailErr(t, "read repaired view", err)
	if !ok || repairedView.Continuation == nil || repairedView.Continuation.SummaryThroughOrd <= 0 {
		t.Fatalf("expected canonical rebuild to restore enriched continuation, got ok=%v view=%+v", ok, repairedView)
	}
	testutil.FailErr(t, "close reopen 2", reopen2.Close())

	reopen3 := BuildForTestAt(t, storeDir, WithLLMClient(client))
	reopen3.Start(t)
	reopen3Ctx := reopen3.OwnerCtx(t, ctx)
	secondReopenView, ok, err := reopen3.Store.Compaction.GetCompactionView(reopen3Ctx, sess.ID)
	testutil.FailErr(t, "read view on second reopen after repair", err)
	if !ok || secondReopenView.Generation != repairedView.Generation {
		t.Fatalf("second reopen did not reuse repaired view: ok=%v gotGen=%d wantGen=%d", ok, secondReopenView.Generation, repairedView.Generation)
	}
	testutil.FailErr(t, "close reopen 3", reopen3.Close())

	t.Logf("RE04_CANDIDATE_METRICS dispatches=%d intermediate_wait_dispatches=0 system_bytes=%d full_req2_bytes=%d full_req3_bytes=%d common_prefix_bytes=%d root_policy_span=[%d..%d] standing_bp_byte=%d req2_history_bp_byte=%d req3_history_bp_byte=%d breakpoints_req2=%+v breakpoints_req3=%+v resumed_bytes=%d continuation_tokens=%d generic_continuation_rewrites=0 enrichment_publications=%d synth_calls_initial=%d summary_through_ord=%d",
		len(reqsAfterResume), len(ser2.SystemJSON), len(ser2.FullJSON), len(ser3.FullJSON), sharedPrefixBytes, rootStart, rootEnd, ser2.StandingMarkerByte, ser2.HistoryMarkerByte, ser3.HistoryMarkerByte, ser2.Breakpoints, ser3.Breakpoints, len(serResumed.FullJSON), continuationTokens, enrichmentPublishes, synthCountAfterEnrich, view1.Continuation.SummaryThroughOrd)
}
