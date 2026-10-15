package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type nativeRequestBudgets struct{ provider *ollama.Provider }

func (b nativeRequestBudgets) RequestBudget(ctx context.Context, _ string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	return b.provider.RequestBudget(ctx, req)
}

func TestCompleteRequestFitsColdOllamaWindowWithoutChangingFrame(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			t.Errorf("budget planning attempted inference: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()
	provider := ollama.New("local", server.URL+"/v1", "", []modelinfo.Entry{{ID: "gemma4:e4b", ContextLength: 16384}})
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{ProviderID: "local", Model: "gemma4:e4b"}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	var rows []api.Message
	for i := 0; i < 8; i++ {
		rows = append(rows, api.Message{ID: fmt.Sprintf("answer-%d", i), Role: api.MessageRoleAssistant, Content: "Ready"},
			api.Message{ID: fmt.Sprintf("feedback-%d", i), Role: api.MessageRoleSystem, Content: strings.Repeat("Recorded feedback. ", 200)})
	}
	rows = append(rows, api.Message{ID: "current", Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
		Authority: api.ContentAuthorityUser, Content: "Recover the original observation",
		ContentParts: []api.MessageContentPart{{Content: "Recover the original observation", Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser}}})
	testutil.FailErr(t, "append canonical history", mem.AppendMessages(t.Context(), sess.ID, rows...))
	rows, err = mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read canonical history", err)
	standing := api.Message{ID: "standing", Role: api.MessageRoleSystem, Content: strings.Repeat("Standing instructions. ", 850),
		ContextPinned: true, PromptCacheBreakpoint: api.PromptCacheTierStanding}
	volatile := api.Message{ID: "volatile", Role: api.MessageRoleSystem, Content: "Live host state", ContextPinned: true}
	messages := []api.Message{
		standing,
		{ID: "checkpoint", Role: api.MessageRoleUser, CompactionCheckpoint: true, Origin: api.MessageOriginHost},
		{ID: "continuation", Role: api.MessageRoleAssistant, Content: strings.Repeat("Detailed continuation. ", 300),
			ContentParts: []api.MessageContentPart{{Source: "compaction_summary", Content: strings.Repeat("Detailed continuation. ", 300)}}},
	}
	messages = append(messages, rows...)
	messages = append(messages, volatile)
	req := modelcall.CompletionRequest{Model: sess.Model, Messages: messages, Tools: []tools.ToolMeta{{Name: "read",
		ArgsSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{
			"type": "string", "description": strings.Repeat("Recorded file path. ", 700)}}}}}}
	if _, _, err := provider.Prepare(t.Context(), req, true); !errors.Is(err, failure.ErrProviderContextTooSmall) {
		t.Fatalf("fixture must overflow before request fitting: %v", err)
	}
	cfg := compaction.DefaultCompactionConfig()
	cfg.ModelContextWindow = 12288
	history := &Service{store: mem, Compactor: compaction.NewSimpleCompactor(cfg, nil), recallAvailable: func(context.Context, *api.Session) bool { return true }}
	fitted, err := history.FitRequest(t.Context(), sess, rows, req, nativeRequestBudgets{provider})
	testutil.FailErr(t, "fit complete request", err)
	payload, _, err := provider.Prepare(t.Context(), fitted, true)
	testutil.FailErr(t, "prepare fitted native request", err)
	if payload.Options.NumCtx > 16384 || payload.Options.NumPredict <= 0 {
		t.Fatalf("invalid native reservation: %+v", payload.Options)
	}
	if !reflect.DeepEqual(fitted.Messages[0], standing) || !reflect.DeepEqual(fitted.Messages[len(fitted.Messages)-1], volatile) || !reflect.DeepEqual(fitted.Tools, req.Tools) {
		t.Fatal("history fitting changed standing, tools, cache tiers, or live host state")
	}
	current, recovery := 0, false
	for _, row := range fitted.Messages {
		if row.ID == "current" {
			current++
			if row.Content != rows[len(rows)-1].Content || !reflect.DeepEqual(row.ContentParts, rows[len(rows)-1].ContentParts) || row.Authority != api.ContentAuthorityUser {
				t.Fatal("fitting changed the original human instruction")
			}
		}
		recovery = recovery || strings.Contains(row.Content, `recall(history=`)
	}
	if current != 1 || !recovery {
		t.Fatal("fitted request lost the active instruction or canonical recovery")
	}
	canonical, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read preserved canonical history", err)
	if !reflect.DeepEqual(canonical, rows) {
		t.Fatal("request fitting changed canonical records")
	}
}

type catalogRequestBudgets struct{ catalog modelcall.Catalog }

func (b catalogRequestBudgets) RequestBudget(ctx context.Context, _ string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	return b.catalog.RequestBudget(ctx, req)
}

func TestRequestFittingKeepsUnchangedHistoryCacheBoundary(t *testing.T) {
	rows := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "go"},
		{ID: "a1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c1", Name: "read"}}},
		{ID: "t1", Role: api.MessageRoleTool, Content: "result", PromptCacheBreakpoint: api.PromptCacheTierHistory,
			ToolResult: &api.ToolResult{ToolCallID: "c1", Tool: "read", Content: "result"}},
	}
	frame := api.Message{ID: "standing", Role: api.MessageRoleSystem, Content: "Stable instructions", PromptCacheBreakpoint: api.PromptCacheTierStanding}
	req := modelcall.CompletionRequest{Model: "large", Messages: append([]api.Message{frame}, rows...)}
	budgets := catalogRequestBudgets{modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: "large", ContextLength: 1_000_000}})}
	history := &Service{}
	fitted, err := history.FitRequest(t.Context(), &api.Session{ID: "s", Model: "large"}, rows, req, budgets)
	testutil.FailErr(t, "fit large-window request", err)
	if !reflect.DeepEqual(req, fitted) {
		t.Fatal("an admitted frame changed its cache boundary or history")
	}
}

func TestRequestFittingUsesRemainingOutputAfterHistoryIsMinimal(t *testing.T) {
	provider := ollama.New("local", "http://unused.invalid", "", []modelinfo.Entry{{ID: "small", ContextLength: 16384, MaxTokens: 24576}})
	current := api.Message{ID: "current", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Continue"}
	req := modelcall.CompletionRequest{Model: "small", Messages: []api.Message{{ID: "standing", Role: api.MessageRoleSystem}, current}}
	low, high := 1, 70000
	for low < high {
		mid := (low + high) / 2
		req.Messages[0].Content = strings.Repeat("a", mid)
		budget := provider.RequestBudget(t.Context(), req)
		available := budget.Window - budget.Headroom - budget.Input
		if available >= 256 {
			low = mid + 1
			continue
		}
		high = mid
	}
	req.Messages[0].Content = strings.Repeat("a", low)
	budget := provider.RequestBudget(t.Context(), req)
	available := budget.Window - budget.Headroom - budget.Input
	if available < 128 {
		t.Fatalf("fixture skipped usable output allocation: %d", available)
	}
	history := &Service{}
	fitted, err := history.FitRequest(t.Context(), &api.Session{ID: "session", ProviderID: "local", Model: "small"}, []api.Message{current}, req, nativeRequestBudgets{provider})
	testutil.FailErr(t, "fit minimal history", err)
	if fitted.MaxTokens != available || len(fitted.Messages) != 2 || !reflect.DeepEqual(req.Messages[0], fitted.Messages[0]) || fitted.Messages[1].Content != current.Content {
		t.Fatalf("minimal history fit changed input or output allocation: %d, want %d", fitted.MaxTokens, available)
	}
	req.MaxTokens = 512
	if _, err := history.FitRequest(t.Context(), &api.Session{ID: "session", ProviderID: "local", Model: "small"}, []api.Message{current}, req, nativeRequestBudgets{provider}); !errors.Is(err, failure.ErrProviderContextTooSmall) {
		t.Fatalf("explicit output allocation must remain required: %v", err)
	}
	req.MaxTokens = 0
	req.ThinkingOverride = &modelcall.ThinkingOverride{BudgetTokens: new(1024)}
	if _, err := history.FitRequest(t.Context(), &api.Session{ID: "session", ProviderID: "local", Model: "small"}, []api.Message{current}, req, nativeRequestBudgets{provider}); !errors.Is(err, failure.ErrProviderContextTooSmall) {
		t.Fatalf("human thinking allocation must remain required: %v", err)
	}
}

func TestEveryRequestAdoptsPublishedHistoryProjection(t *testing.T) {
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{ProviderID: "fixture", Model: "large"}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append original history", mem.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "old", Role: api.MessageRoleAssistant, Content: "Original detail"},
		api.Message{ID: "current", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Continue"}))
	canonical, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read original history", err)
	summary := api.Message{ID: "summary", Role: api.MessageRoleAssistant, Content: "Recorded continuation",
		ContentParts: []api.MessageContentPart{{Source: "compaction_summary", Content: "Recorded continuation"}}}
	testutil.FailErr(t, "publish projection", mem.PutCompactionView(t.Context(), sess.ID, store.CompactionView{
		Generation: 1, CoveredThroughOrd: canonical[0].Ord, CoveredThroughID: canonical[0].ID, SourceSeq: canonical[1].Seq,
		Messages: []api.Message{summary}}))
	history := &Service{store: mem}
	budgets := catalogRequestBudgets{modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: "large", ContextLength: 1_000_000}})}
	for range 2 {
		req := modelcall.CompletionRequest{Model: "large", Messages: canonical}
		fitted, err := history.FitRequest(t.Context(), sess, canonical, req, budgets)
		testutil.FailErr(t, "fit refreshed request", err)
		if len(fitted.Messages) != 2 || fitted.Messages[0].ID != summary.ID || fitted.Messages[1].ID != "current" || sess.CompactionGeneration != 1 {
			t.Fatal("request lost its published continuation after canonical history refresh")
		}
	}
}

func TestActiveToolBodyCanBecomeARecoverableProjection(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()
	provider := ollama.New("local", server.URL+"/v1", "", []modelinfo.Entry{{ID: "gemma4:e4b", ContextLength: 16384}})
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{ProviderID: "local", Model: "gemma4:e4b"}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create active session", err)
	args := map[string]any{"path": "observations.txt", "offset": float64(650), "limit": float64(151)}
	body := strings.Repeat("Recorded observation. ", 6000)
	recallBody := strings.Repeat("Recovered page. ", 200)
	testutil.FailErr(t, "append active exchange", mem.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "current", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Read the observations"},
		api.Message{ID: "call", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "read-call", Name: "read", Args: args}}},
		api.Message{ID: "result", Role: api.MessageRoleTool, Content: body, TrustTier: api.ContentTrustTierTrusted,
			ContentParts: []api.MessageContentPart{{Content: body, TrustTier: api.ContentTrustTierUntrusted}},
			ToolResult:   &api.ToolResult{Tool: "read", ToolCallID: "read-call", AssistantMessageID: "call", ToolArgs: args, Content: body, Outcome: api.ToolResultOutcomeCompleted}},
		api.Message{ID: "recall-call", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "rc-1", Name: "recall"}}},
		api.Message{ID: "recall-result", Role: api.MessageRoleTool, Content: recallBody, TrustTier: api.ContentTrustTierUntrusted,
			ToolResult: &api.ToolResult{Tool: "recall", ToolCallID: "rc-1", AssistantMessageID: "recall-call", Content: recallBody, Outcome: api.ToolResultOutcomeCompleted}}))
	rows, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "load active exchange", err)
	standing := api.Message{ID: "standing", Role: api.MessageRoleSystem, ContextPinned: true, Content: strings.Repeat("Standing instructions. ", 850)}
	req := modelcall.CompletionRequest{Model: sess.Model, Messages: append([]api.Message{standing}, rows...)}
	history := &Service{store: mem, recallAvailable: func(context.Context, *api.Session) bool { return true }}
	fitted, err := history.FitRequest(t.Context(), sess, rows, req, nativeRequestBudgets{provider})
	testutil.FailErr(t, "fit active observation", err)
	_, _, err = provider.Prepare(t.Context(), fitted, true)
	testutil.FailErr(t, "prepare projected active observation", err)
	call, result, recallKept := false, false, false
	for _, row := range fitted.Messages {
		if row.ID == "call" {
			call = reflect.DeepEqual(row.ToolCalls, rows[1].ToolCalls)
		}
		if row.ID == "result" {
			result = row.ToolResult != nil && row.ToolResult.ToolCallID == "read-call" && row.ToolResult.AssistantMessageID == "call" && row.ToolResult.Outcome == api.ToolResultOutcomeCompleted && reflect.DeepEqual(row.ToolResult.ToolArgs, args)
			if !strings.Contains(row.Content, `recall(history=`) || row.TrustTier != api.ContentTrustTierUntrusted || row.Authority != api.ContentAuthorityNone {
				t.Fatal("active projection lost recovery or promoted observation trust")
			}
		}
		if row.ID == "recall-result" {
			recallKept = row.Content == recallBody
		}
	}
	if !call || !result || !recallKept {
		t.Fatal("active projection changed call pairing, arguments, outcome, or bounded recall result")
	}
	canonical, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "reload original observation", err)
	if !reflect.DeepEqual(rows, canonical) {
		t.Fatal("active projection changed canonical tool records")
	}
}

func TestReviewRecallPageProjectionKeepsCursorContiguous(t *testing.T) {
	body := strings.Repeat("😀", 4096)
	raw, err := json.Marshal(map[string]any{"history": map[string]any{"format": "record", "entries": []any{map[string]any{"message_id": "original", "ord": 1, "content": body, "runes": 9000, "offset": 4096, "seq": 1}}, "next_offset": 8192, "through_ord": 1, "recorded": true}})
	testutil.FailErr(t, "encode record page", err)
	rows := projectRecordedToolBodies(t.Context(), []api.Message{{ID: "recall-result", Ord: 3, Role: api.MessageRoleTool, Content: string(raw), ToolResult: &api.ToolResult{Tool: "recall", Content: string(raw)}}})
	var page struct {
		History struct {
			Entries []struct {
				Content string `json:"content"`
				Offset  int    `json:"offset"`
			} `json:"entries"`
			Next int `json:"next_offset"`
		} `json:"history"`
	}
	testutil.FailErr(t, "decode projected page", json.Unmarshal([]byte(rows[0].Content), &page))
	if len(page.History.Entries) == 0 {
		t.Fatalf("projection discarded page entries: %s", rows[0].Content)
	}
	entry := page.History.Entries[0]
	if got := entry.Offset + utf8.RuneCountInString(entry.Content); got != page.History.Next {
		t.Fatalf("projected page has %d Unicode characters and offset %d but next_offset=%d; %d characters are skipped", utf8.RuneCountInString(entry.Content), entry.Offset, page.History.Next, page.History.Next-got)
	}
	indexRaw, err := json.Marshal(map[string]any{"history": map[string]any{"format": "index", "entries": []any{
		map[string]any{"message_id": "m1", "ord": 1, "content": strings.Repeat("a", 800)},
		map[string]any{"message_id": "m2", "ord": 2, "content": strings.Repeat("b", 800)},
		map[string]any{"message_id": "m3", "ord": 3, "content": strings.Repeat("c", 800)},
		map[string]any{"message_id": "m4", "ord": 4, "content": strings.Repeat("d", 15000)},
	}, "through_ord": 4, "recorded": true}})
	testutil.FailErr(t, "encode index page", err)
	indexArgs := map[string]any{"history": map[string]any{"through_ord": 4, "search": "needle"}, "limit": 4}
	indexRows := projectRecordedToolBodies(t.Context(), []api.Message{{ID: "recall-index", Ord: 4, Role: api.MessageRoleTool, Content: string(indexRaw), ToolResult: &api.ToolResult{Tool: "recall", Content: string(indexRaw), ToolArgs: indexArgs}}})
	var indexPage struct {
		History     recall.HistoryPage          `json:"history"`
		NextRequest *recall.HistoryContinuation `json:"next_request"`
	}
	testutil.FailErr(t, "decode projected index page", json.Unmarshal([]byte(indexRows[0].Content), &indexPage))
	if len(indexPage.History.Entries) == 0 || indexPage.History.Entries[len(indexPage.History.Entries)-1].Ord != indexPage.History.NextAfterOrd {
		t.Fatalf("projected index page lost contiguous next_after_ord: %+v", indexPage.History)
	}
	if next := indexPage.NextRequest; next == nil || next.History.AfterOrd != indexPage.History.NextAfterOrd || next.History.Search != "needle" || next.Limit != 4 {
		t.Fatalf("projected index lost its complete continuation: %+v", next)
	}
	if _, changed := indexArgs["history"].(map[string]any)["after_ord"]; changed {
		t.Fatal("projection changed the recorded invocation")
	}
}

func TestReviewRequestBudgetCountsAttachedImages(t *testing.T) {
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Describe the images"}}}
	baseline := modelcall.EstimateRequestInput(req)
	req.Messages[0].ArtifactIDs = []string{"image-1", "image-2", "image-3"}
	got := modelcall.EstimateRequestInput(req)
	want := baseline + 3*providerwire.UnsizedImageTokenEstimate
	if got < want {
		t.Fatalf("budget=%d without images=%d; expected at least %d with 3 attachments", got, baseline, want)
	}
	req.Messages = append(req.Messages,
		api.Message{Role: api.MessageRoleAssistant, ModelReasoning: &api.ModelReasoning{Text: "inspecting", Details: []json.RawMessage{json.RawMessage(`"detail"`)}}},
		api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Visual: &api.VisualArtifact{Perceive: true}}},
	)
	if withToolVisual := modelcall.EstimateRequestInput(req); withToolVisual < got+providerwire.UnsizedImageTokenEstimate {
		t.Fatalf("budget with perceived tool image = %d, want at least %d", withToolVisual, got+providerwire.UnsizedImageTokenEstimate)
	}
}

func TestReviewFittingHonorsProviderHeadroom(t *testing.T) {
	req := modelcall.CompletionRequest{Model: "small", MaxTokens: 512, Messages: []api.Message{{ID: "standing", Role: api.MessageRoleSystem, Content: strings.Repeat("frame ", 1800)}}}
	budgets := catalogRequestBudgets{modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: "small", ContextLength: 4096}})}
	svc := &Service{}
	fitted, err := svc.FitRequest(t.Context(), &api.Session{ID: "s", Model: "small"}, nil, req, budgets)
	if err != nil {
		t.Logf("correctly refused required frame: %v", err)
		return
	}
	actual := budgets.RequestBudget(t.Context(), "fixture", fitted)
	if actual.Input+actual.Output+actual.Headroom > actual.Window {
		t.Fatalf("fit admitted input=%d output=%d headroom=%d window=%d, exceeding provider contract by %d", actual.Input, actual.Output, actual.Headroom, actual.Window, actual.Input+actual.Output+actual.Headroom-actual.Window)
	}
}

func TestRequestFittingReservesObservedInputUsage(t *testing.T) {
	current := api.Message{ID: "current", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: strings.Repeat("Measured input. ", 600)}
	rows := []api.Message{current}
	req := modelcall.CompletionRequest{Model: "small", Messages: rows}
	budgets := catalogRequestBudgets{modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: "small", ContextLength: 8192}})}
	service := &Service{}
	sess := &api.Session{ID: "observed", Model: "small"}
	estimate := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(rows))
	service.ObserveTokens(sess.ID, sess.ProviderID, sess.Model, estimate*2, estimate)
	fitted, err := service.FitRequest(t.Context(), sess, rows, req, budgets)
	testutil.FailErr(t, "fit observed input usage", err)
	budget := budgets.RequestBudget(t.Context(), "fixture", fitted)
	observed := service.Calibration(t.Context(), sess).ProjectBilled(compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(fitted.Messages)), 0)
	observed += (observed + 9) / 10
	if max(observed, budget.Input)+budget.Output+budget.Headroom > budget.Window {
		t.Fatalf("observed input exceeded fitted window: observed=%d budget=%+v", observed, budget)
	}
	if !reflect.DeepEqual(rows, fitted.Messages) || fitted.MaxTokens <= 0 {
		t.Fatal("output allocation changed the active instruction or was not bounded")
	}
	req.MaxTokens = 4096
	if _, err := service.FitRequest(t.Context(), sess, rows, req, budgets); !errors.Is(err, failure.ErrProviderContextTooSmall) {
		t.Fatalf("explicit output must not overcommit observed input: %v", err)
	}
}

func TestRequestFittingKeepsToolBodyWhenOutputCanYield(t *testing.T) {
	body := strings.Repeat("Recorded observation. ", 1000)
	rows := []api.Message{
		{ID: "intent", Ord: 1, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Inspect the observations"},
		{ID: "call", Ord: 2, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "read-call", Name: "read"}}},
		{ID: "result", Ord: 3, Role: api.MessageRoleTool, Content: body, PromptCacheBreakpoint: api.PromptCacheTierHistory,
			ToolResult: &api.ToolResult{ToolCallID: "read-call", Tool: "read", Content: body}},
	}
	toolDefs := []tools.ToolMeta{{Name: "read", Description: strings.Repeat("Tool schema. ", 800)}}
	req := modelcall.CompletionRequest{Model: "small", Messages: rows, Tools: toolDefs}
	budgets := catalogRequestBudgets{modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: "small", ContextLength: 16384}})}
	service := &Service{store: store.NewMemory(), recallAvailable: func(context.Context, *api.Session) bool { return true }}
	sess := &api.Session{ID: "s", Model: "small"}
	initialTranscript := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(rows[:1]))
	toolTokens := modelcall.EstimateRequestInput(modelcall.CompletionRequest{Tools: toolDefs})
	service.ObserveTokens(sess.ID, sess.ProviderID, sess.Model, initialTranscript+toolTokens, initialTranscript)
	fitted, err := service.FitRequest(t.Context(), sess, rows, req, budgets)
	testutil.FailErr(t, "allocate output around complete observations", err)
	if !reflect.DeepEqual(rows, fitted.Messages) || fitted.MaxTokens <= 0 {
		t.Fatal("automatic output reservation or tool-schema calibration displaced a tool body that fits")
	}
	budget := budgets.RequestBudget(t.Context(), "fixture", fitted)
	if budget.Input+budget.Output+budget.Headroom > budget.Window {
		t.Fatalf("unprojected observations exceeded fitted window: %+v", budget)
	}
}

func TestRequestFittingDiscardsOtherReceiverCalibration(t *testing.T) {
	rows := []api.Message{{ID: "current", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Continue the task"}}
	req := modelcall.CompletionRequest{Model: "small", MaxTokens: 512, Messages: rows}
	budgets := catalogRequestBudgets{modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: "small", ContextLength: 4096}})}
	for _, receiver := range []struct{ name, providerID, model string }{
		{"provider", "other", "small"}, {"model", "fixture", "large"},
	} {
		t.Run(receiver.name, func(t *testing.T) {
			service := &Service{}
			sess := &api.Session{ID: "switch", ProviderID: receiver.providerID, Model: receiver.model}
			estimate := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(rows))
			service.ObserveTokens(sess.ID, sess.ProviderID, sess.Model, 100000, estimate)
			if !service.Calibration(t.Context(), sess).Calibrated() {
				t.Fatal("matching receiver lost its observation")
			}
			sess.ProviderID, sess.Model = "fixture", "small"
			if service.Calibration(t.Context(), sess).Calibrated() {
				t.Fatal("receiver switch reused incompatible input usage")
			}
			fitted, err := service.FitRequest(t.Context(), sess, rows, req, budgets)
			testutil.FailErr(t, "fit new receiver without old token ratio", err)
			if !reflect.DeepEqual(fitted.Messages, rows) || fitted.MaxTokens != 512 {
				t.Fatal("receiver switch unnecessarily changed the request")
			}
		})
	}
}

func TestRequestFittingCanProjectRecallBelowSixteenKilobytes(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	raw, err := json.Marshal(map[string]any{"resolution": "matched", "hits": []any{map[string]any{"snippet": strings.Repeat("recorded observation ", 500)}}})
	testutil.FailErr(t, "encode recall observation", err)
	if len(raw) <= 2048 || len(raw) >= 16384 {
		t.Fatal("fixture must exercise the medium recall size")
	}
	rows := []api.Message{
		{ID: "current", Ord: 1, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Recover the original observation"},
		{ID: "call", Ord: 2, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "recall-call", Name: "recall"}}},
		{ID: "result", Ord: 3, Role: api.MessageRoleTool, Content: string(raw), ToolResult: &api.ToolResult{ToolCallID: "recall-call", Tool: "recall", Content: string(raw)}},
	}
	frame := api.Message{ID: "frame", Role: api.MessageRoleSystem, Content: strings.Repeat("standing instruction ", 800)}
	req := modelcall.CompletionRequest{Model: "small", MaxTokens: 512, Messages: append([]api.Message{frame}, rows...)}
	budgets := catalogRequestBudgets{modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: "small", ContextLength: 8192}})}
	service := &Service{store: store.NewMemory(), recallAvailable: func(context.Context, *api.Session) bool { return true }}
	fitted, err := service.FitRequest(t.Context(), &api.Session{ID: "s", Model: "small"}, rows, req, budgets)
	testutil.FailErr(t, "fit medium recall around required frame", err)
	budget := budgets.RequestBudget(t.Context(), "fixture", fitted)
	if budget.Input+budget.Output+budget.Headroom > budget.Window {
		t.Fatalf("recall exceeded receiver window: %+v", budget)
	}
	var result string
	for _, row := range fitted.Messages {
		if row.ID == "result" {
			result = row.Content
		}
	}
	if result == "" || result == string(raw) || !strings.Contains(result, "recall(history=") {
		t.Fatal("active recall result lost its bounded canonical recovery")
	}
	if rows[2].Content != string(raw) || rows[2].ToolResult.Content != string(raw) {
		t.Fatal("request fitting changed the recorded observation")
	}
}
