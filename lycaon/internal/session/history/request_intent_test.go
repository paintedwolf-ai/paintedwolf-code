package history

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const rewordedContinuationTemplate = `{% if level == "references" %}{% if omitted_through_ord %}Gap {{ omitted_from_ord }}..{{ omitted_through_ord }} unsummarized.{% elif through_ord %}Covered <= {{ through_ord }}.{% else %}Prior context omitted.{% endif %}
{% if can_recall_history and through_ord %}Page ` + "`" + `recall(history={"after_ord":0,"through_ord":{{ through_ord }}})` + "`" + `{% else %}Re-read live sources.{% endif %}{% else %}Summary through {{ through_ord }}
{% for s in compacted_sources %}{% for h in s.handles %}{{ h }}{% if can_recall_query %} -> ` + "`" + `recall(query="handle:{{ h }}")` + "`" + `{% endif %}{% endfor %}{% endfor %}{% if can_recall_history %} · ` + "`" + `recall(history={"after_ord":0,"through_ord":{{ through_ord }}})` + "`" + `{% endif %}{% endif %}`

func installRewordedContinuationRenderer(t *testing.T) {
	t.Helper()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	testutil.FailErr(t, "register reworded continuation template", engine.Register("guidance/compaction/continuation-record.md", rewordedContinuationTemplate))
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
	t.Cleanup(func() { guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t)) })
}

func TestRequestRestoresInstructionMissingFromAssembledMessages(t *testing.T) {
	opener := api.Message{ID: "request", Ord: 2, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Build a game"}
	capture := api.Message{ID: "capture", Ord: 11, Role: api.MessageRoleTool}
	history := []api.Message{opener, capture}
	req := modelcall.CompletionRequest{Messages: []api.Message{{ID: "system", Role: api.MessageRoleSystem}, capture, {ID: "procedure", Role: api.MessageRoleSystem}}}
	_, restored, err := prepareRequestHistory(t.Context(), nil, &api.Session{}, history, req)
	testutil.FailErr(t, "restore active request", err)
	if len(restored.Messages) != 4 || restored.Messages[1].ID != opener.ID || !restored.Messages[1].ContextPinned {
		t.Fatalf("active request not restored in history slot: %+v", restored.Messages)
	}
	testutil.FailErr(t, "validate restored request", validateRequestIntent(history, restored.Messages))
	if err := validateRequestIntent(history, req.Messages); err == nil {
		t.Fatal("missing request passed validation")
	}
	if history[0].ContextPinned {
		t.Fatal("request preparation mutated canonical history")
	}
}

func TestRequestIntentSurvivesViewReplacementWithoutDuplication(t *testing.T) {
	opener := api.Message{ID: "request", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Run it in a container"}
	source := []api.Message{opener}
	rows := []api.Message{{ID: "summary", Role: api.MessageRoleAssistant}, opener}
	for range 3 {
		rows = messageview.PreserveRequestIntent(rows, source)
	}
	if len(rows) != 2 || !rows[1].ContextPinned {
		t.Fatalf("repeated projection = %+v", rows)
	}
	if got := messageview.PreserveRequestIntent(nil, source); len(got) != 1 || got[0].Content != opener.Content {
		t.Fatalf("empty projection = %+v", got)
	}
	if got := messageview.PreserveRequestIntent(rows, nil); len(got) != 2 {
		t.Fatal("missing source changed history")
	}
	testutil.FailErr(t, "validate host-only request", validateRequestIntent(nil, nil))
}

func TestRestoredRequestNamesOmittedHistory(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	opener := api.Message{ID: "request", Ord: 2, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Build a game"}
	middle := api.Message{ID: "earlier", Ord: 3, Role: api.MessageRoleAssistant, Content: "Earlier work"}
	latest := api.Message{ID: "latest", Ord: 4, Role: api.MessageRoleAssistant, Content: "Recent work"}
	source := []api.Message{opener, middle, latest}
	req := modelcall.CompletionRequest{Messages: []api.Message{latest}}
	_, restored, err := prepareRequestHistory(t.Context(), nil, &api.Session{}, source, req)
	testutil.FailErr(t, "restore request and omission", err)
	if len(restored.Messages) != 3 || restored.Messages[0].ID != opener.ID || restored.Messages[1].ID != historyCoverageNoticeID || restored.Messages[2].ID != latest.ID {
		t.Fatalf("restored history = %+v", restored.Messages)
	}
	want, err := omissionNotice(t.Context(), nil, &api.Session{}, historyCoverageNoticeID, 3, 3)
	testutil.FailErr(t, "render missing interval", err)
	if restored.Messages[1].Content != want.Content {
		t.Fatal("omission did not identify missing history")
	}
	_, again, err := prepareRequestHistory(t.Context(), nil, &api.Session{}, source, restored)
	testutil.FailErr(t, "prepare restored request again", err)
	if countID(again.Messages, historyCoverageNoticeID) != 1 {
		t.Fatal("repeated preparation duplicated omission")
	}
}

func TestFitRequestRewordedOmissionNoticeNarrowsRecipientWithoutEnglishParsing(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	svc, sqlStore, sess := newTurnSession(t, cfg)
	svc.SetCompactor(nil)
	installRewordedContinuationRenderer(t)
	svc.recallAvailable = func(context.Context, *api.Session) bool { return true }
	canonical := seedTurn(t, sqlStore, sess.ID, 300, "recorded observation")

	cont := &messageview.Continuation{
		Format:            messageview.ContinuationFormat,
		ThroughOrd:        40,
		SummaryThroughOrd: 20,
		Reason:            "summarized",
		Summary:           messageview.ContinuationSummary{Facts: []string{"inventory indexed"}},
		Sources:           []messageview.ContinuationSource{{MessageID: canonical[2].ID, Tool: "read", Handles: []string{"read#2"}}},
	}
	summaryMsg := api.Message{
		ID:           "summary",
		Role:         api.MessageRoleAssistant,
		Content:      "placeholder",
		ContentParts: []api.MessageContentPart{{Source: "compaction_summary", Content: "placeholder"}},
	}
	testutil.FailErr(t, "put view", sqlStore.Compaction.PutCompactionView(t.Context(), sess.ID, store.CompactionView{
		Generation:        1,
		CoveredThroughOrd: 40,
		CoveredThroughID:  canonical[39].ID,
		SourceSeq:         canonical[len(canonical)-1].Seq,
		Continuation:      cont,
		Messages:          []api.Message{summaryMsg},
	}))

	broadCtx := recallReachCtx(t)
	loaded, err := svc.Load(broadCtx, sess)
	testutil.FailErr(t, "load under broad context", err)
	noticeIdx := indexOfID(loaded, historyCoverageNoticeID)
	if noticeIdx < 0 {
		t.Fatal("expected tail omission notice in loaded history")
	}
	var wantThrough int64
	for _, after := range loaded[noticeIdx+1:] {
		if after.Ord > 40 {
			wantThrough = after.Ord - 1
			break
		}
	}
	wantSignal := fmt.Sprintf("history-omission:41:%d", wantThrough)
	if loaded[noticeIdx].HostSignalID != wantSignal {
		t.Fatalf("HostSignalID = %q, want %q", loaded[noticeIdx].HostSignalID, wantSignal)
	}
	wantGap := fmt.Sprintf("Gap 41..%d unsummarized.", wantThrough)
	if !strings.Contains(loaded[noticeIdx].Content, wantGap) || !strings.Contains(loaded[noticeIdx].Content, "recall(history=") {
		t.Fatalf("broad loaded notice did not render reworded gap + recall(history=...):\n%s", loaded[noticeIdx].Content)
	}

	budgets := catalogRequestBudgets{modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: sess.Model, ContextLength: 196608}})}
	standing := api.Message{ID: "standing", Role: api.MessageRoleSystem, ContextPinned: true, Content: "Standing"}

	noRecallReq := modelcall.CompletionRequest{
		Model:    sess.Model,
		Messages: append([]api.Message{standing}, loaded...),
		Tools:    []tools.ToolMeta{{Name: "read", ArgsSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
	}
	fittedNoRecall, err := svc.FitRequest(broadCtx, sess, loaded, noRecallReq, budgets, nil)
	testutil.FailErr(t, "FitRequest for no-recall recipient", err)
	gotNotice := fittedNoRecall.Messages[indexOfID(fittedNoRecall.Messages, historyCoverageNoticeID)].Content
	if !strings.Contains(gotNotice, wantGap) || !strings.Contains(gotNotice, "Re-read live sources.") {
		t.Fatalf("reworded notice lost exact interval for no-recall recipient:\n%s", gotNotice)
	}
	if joined := joinMessageContents(fittedNoRecall.Messages); strings.Contains(joined, "recall(") {
		t.Fatalf("broader recipient recall call leaked into no-recall request:\n%s", joined)
	}

	queryOnlyReq := modelcall.CompletionRequest{
		Model:    sess.Model,
		Messages: append([]api.Message{standing}, loaded...),
		Tools: []tools.ToolMeta{{
			Name:       "recall",
			ArgsSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
		}},
	}
	fittedQueryOnly, err := svc.FitRequest(broadCtx, sess, loaded, queryOnlyReq, budgets, nil)
	testutil.FailErr(t, "FitRequest for query-only recipient", err)
	gotQueryNotice := fittedQueryOnly.Messages[indexOfID(fittedQueryOnly.Messages, historyCoverageNoticeID)].Content
	if !strings.Contains(gotQueryNotice, wantGap) || strings.Contains(gotQueryNotice, "recall(history=") {
		t.Fatalf("query-only notice lost exact interval or leaked recall(history=...):\n%s", gotQueryNotice)
	}
	joinedQuery := joinMessageContents(fittedQueryOnly.Messages)
	if !strings.Contains(joinedQuery, `recall(query="handle:read#2")`) || strings.Contains(joinedQuery, "recall(history=") {
		t.Fatalf("query-only request did not deliver query-only recall guidance:\n%s", joinedQuery)
	}
}

func TestFitRequestRecoversOmissionIntervalFromCoordinatesAndFallsBackSafely(t *testing.T) {
	budgets := catalogRequestBudgets{modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: "large", ContextLength: 196608}})}

	t.Run("recovers interval from structured history coordinates when HostSignalID is unset", func(t *testing.T) {
		assertCoordinateOmissionRecovery(t, budgets)
	})
	t.Run("unrecoverable interval renders safe factual coverage without stale callable guidance", func(t *testing.T) {
		assertUnrecoverableOmissionFallback(t, budgets)
	})
}

func assertCoordinateOmissionRecovery(t *testing.T, budgets RequestBudgets) {
	t.Helper()
	installRewordedContinuationRenderer(t)
	repos := storetest.New(t, testdbseed.DefaultProjectID)
	sess, err := repos.Sessions.Create(t.Context(), api.CreateSessionRequest{ProviderID: "fixture", Model: "large"}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	rows := []api.Message{{ID: "opener", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Implement feature"}}
	for i := 2; i <= 14; i++ {
		rows = append(rows, api.Message{ID: fmt.Sprintf("m-%d", i), Role: api.MessageRoleAssistant, Content: fmt.Sprintf("obs %d", i)})
	}
	testutil.FailErr(t, "append rows", repos.Transcript.AppendMessages(t.Context(), sess.ID, rows...))
	canonical, err := repos.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "get messages", err)

	testutil.FailErr(t, "put view", repos.Compaction.PutCompactionView(t.Context(), sess.ID, store.CompactionView{
		Generation:        1,
		CoveredThroughOrd: 4,
		CoveredThroughID:  canonical[3].ID,
		SourceSeq:         canonical[len(canonical)-1].Seq,
		Messages: []api.Message{{
			ID:           "sum",
			Role:         api.MessageRoleAssistant,
			Content:      "Summary",
			ContentParts: []api.MessageContentPart{{Source: "compaction_summary", Content: "Summary"}},
		}},
	}))

	staleNotice := api.Message{
		ID:             historyCoverageNoticeID,
		Role:           api.MessageRoleSystem,
		Origin:         api.MessageOriginHost,
		Authority:      api.ContentAuthoritySystem,
		TrustTier:      api.ContentTrustTierTrusted,
		ContextPinned:  true,
		ModelProjected: true,
		ContentParts: []api.MessageContentPart{{
			Source:    "content_authority_notice",
			Content:   "Authority header",
			Origin:    api.MessageOriginHost,
			Authority: api.ContentAuthoritySystem,
			TrustTier: api.ContentTrustTierTrusted,
		}},
		Content: "Authority header\n\nCustom wording without ordinals.\nPage `recall(history={\"after_ord\":0,\"through_ord\":12})`",
	}
	assembled := []api.Message{staleNotice, canonical[0], canonical[12], canonical[13]}
	svc := &Service{
		sessionStore:    repos.Sessions,
		transcriptStore: repos.Transcript,
		compactionStore: repos.Compaction,
		recallAvailable: func(context.Context, *api.Session) bool { return true },
	}
	req := modelcall.CompletionRequest{
		Model:    "large",
		Messages: assembled,
		Tools:    []tools.ToolMeta{{Name: "read", ArgsSchema: map[string]any{"type": "object"}}},
	}
	fitted, err := svc.FitRequest(t.Context(), sess, assembled, req, budgets, nil)
	testutil.FailErr(t, "FitRequest coordinate recovery", err)
	gotRow := fitted.Messages[indexOfID(fitted.Messages, historyCoverageNoticeID)]
	if !strings.HasPrefix(gotRow.Content, "Authority header\n\n") || !strings.Contains(gotRow.Content, "Gap 5..12 unsummarized.") || strings.Contains(gotRow.Content, "recall(") {
		t.Fatalf("structured coordinate recovery failed: %q", gotRow.Content)
	}
}

func assertUnrecoverableOmissionFallback(t *testing.T, budgets RequestBudgets) {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	staleNotice := api.Message{
		ID:            historyCoverageNoticeID,
		Role:          api.MessageRoleSystem,
		Origin:        api.MessageOriginHost,
		Authority:     api.ContentAuthoritySystem,
		TrustTier:     api.ContentTrustTierTrusted,
		ContextPinned: true,
		Content:       "Unparseable notice text with `recall(history={\"after_ord\":0,\"through_ord\":99})`",
	}
	unnumbered := []api.Message{
		{ID: "u-unnumbered", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Continue"},
		staleNotice,
		{ID: "a-unnumbered", Role: api.MessageRoleAssistant, Content: "Working"},
	}
	svc := &Service{recallAvailable: func(context.Context, *api.Session) bool { return true }}
	req := modelcall.CompletionRequest{
		Model:    "large",
		Messages: unnumbered,
		Tools:    []tools.ToolMeta{recallToolMeta(t)},
	}
	fitted, err := svc.FitRequest(t.Context(), &api.Session{ID: "s", Model: "large"}, unnumbered, req, budgets, nil)
	testutil.FailErr(t, "FitRequest unrecoverable fallback", err)
	gotNotice := fitted.Messages[indexOfID(fitted.Messages, historyCoverageNoticeID)].Content
	if !strings.Contains(gotNotice, "[host:earlier history omitted; no summary covers it]") || !strings.Contains(gotNotice, "Reacquire omitted observations before relying on them.") {
		t.Fatalf("fallback notice missing factual coverage message:\n%s", gotNotice)
	}
	if strings.Contains(gotNotice, "recall(") || strings.Contains(gotNotice, "through_ord") {
		t.Fatalf("unrecoverable fallback replayed stale or invalid recall guidance:\n%s", gotNotice)
	}

	withCoveredView, err := restoreRequestOmissions(t.Context(), &store.CompactionView{CoveredThroughOrd: 15}, unnumbered, unnumbered, false)
	testutil.FailErr(t, "fallback with covered view watermark", err)
	coveredNotice := withCoveredView[indexOfID(withCoveredView, historyCoverageNoticeID)].Content
	if !strings.Contains(coveredNotice, "[host:compacted history through ordinal 15]") || strings.Contains(coveredNotice, "recall(") {
		t.Fatalf("covered-view fallback lost factual watermark or leaked recall: %q", coveredNotice)
	}
}
