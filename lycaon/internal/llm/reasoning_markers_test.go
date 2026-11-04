package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

var thinkMarkers = modelinfo.ReasoningMarkers{Open: "<think>", Close: "</think>"}

// readAll feeds deltas in order and returns the classified text, with a
// terminal flush.
func readAll(markers modelinfo.ReasoningMarkers, chunks ...modelcall.StreamChunk) (visible, reasoning string) {
	reader := newReasoningMarkerReader(markers)
	var v, r strings.Builder
	for i, c := range chunks {
		if i == len(chunks)-1 {
			c.Done = true
		}
		mapped, keep := reader.chunk(c)
		if !keep {
			continue
		}
		v.WriteString(mapped.Content)
		r.WriteString(mapped.Reasoning)
	}
	return v.String(), r.String()
}

func content(s string) modelcall.StreamChunk   { return modelcall.StreamChunk{Content: s} }
func reasoning(s string) modelcall.StreamChunk { return modelcall.StreamChunk{Reasoning: s} }

// Together streams Qwen's open tag and a newline as content, the reasoning in
// its own field, and then the answer: the tag stands alone.
func TestReasoningMarkerReaderOpenTagBesideSideChannelReasoning(t *testing.T) {
	visible, reasoning := readAll(thinkMarkers,
		content("<think>"), content("\n"), reasoning("The user wants a list."), reasoning(" Use the tool."),
		modelcall.StreamChunk{ToolCalls: []api.ToolCall{{ID: "1", Name: "list_dir"}}},
		content("I'll list the directory."),
	)
	if visible != "I'll list the directory." {
		t.Fatalf("visible = %q, want the answer without the tag", visible)
	}
	if reasoning != "The user wants a list. Use the tool." {
		t.Fatalf("reasoning = %q, want only the side-channel reasoning", reasoning)
	}
}

// A repeated open tag and whitespace around it are template noise.
func TestReasoningMarkerReaderDropsRepeatedOpenTags(t *testing.T) {
	visible, reasoning := readAll(thinkMarkers,
		content("<think>\n<think>"), reasoning("plan"), content("\n\nDone."),
	)
	if visible != "Done." || reasoning != "plan" {
		t.Fatalf("visible = %q reasoning = %q", visible, reasoning)
	}
}

// A stack that reports no reasoning field leaves the whole block in the
// content; the tags bound it and the text after the close tag is visible.
func TestReasoningMarkerReaderInlineBlock(t *testing.T) {
	visible, reasoning := readAll(thinkMarkers,
		content("<think>\nCount the files"), content(" first.\n</think>\n\nThere are three files."),
	)
	if visible != "There are three files." {
		t.Fatalf("visible = %q", visible)
	}
	if reasoning != "Count the files first.\n" {
		t.Fatalf("reasoning = %q", reasoning)
	}
}

// Tags split across deltas are still read as one tag.
func TestReasoningMarkerReaderHoldsPartialTags(t *testing.T) {
	visible, reasoning := readAll(thinkMarkers,
		content("<th"), content("ink>think"), content("ing</th"), content("ink>answer"),
	)
	if visible != "answer" || reasoning != "thinking" {
		t.Fatalf("visible = %q reasoning = %q", visible, reasoning)
	}
}

// A tag that never completes is ordinary text, as is content without a tag.
func TestReasoningMarkerReaderPassesPlainText(t *testing.T) {
	visible, reasoning := readAll(thinkMarkers, content("<thin"), content("g is fine"))
	if visible != "<thing is fine" || reasoning != "" {
		t.Fatalf("visible = %q reasoning = %q", visible, reasoning)
	}
	visible, reasoning = readAll(thinkMarkers, content("  Plain answer <think> later"))
	if visible != "  Plain answer <think> later" || reasoning != "" {
		t.Fatalf("visible = %q reasoning = %q, want a tag past the lead left alone", visible, reasoning)
	}
}

// An open block the stream never closes was all reasoning.
func TestReasoningMarkerReaderUnclosedBlockIsReasoning(t *testing.T) {
	visible, reasoning := readAll(thinkMarkers, content("<think>never done"))
	if visible != "" || reasoning != "never done" {
		t.Fatalf("visible = %q reasoning = %q", visible, reasoning)
	}
}

// A stray close tag after side-channel reasoning is dropped with the
// whitespace around it.
func TestReasoningMarkerReaderDropsStrayCloseTag(t *testing.T) {
	visible, reasoning := readAll(thinkMarkers,
		content("<think>"), reasoning("r"), content("\n</think>\n"), content("Answer"),
	)
	if visible != "Answer" || reasoning != "r" {
		t.Fatalf("visible = %q reasoning = %q", visible, reasoning)
	}
}

// Reasoning that arrives before the open tag still settles the block.
func TestReasoningMarkerReaderSideChannelBeforeTag(t *testing.T) {
	visible, reasoning := readAll(thinkMarkers, reasoning("early"), content("<think>\nAnswer"))
	if visible != "Answer" || reasoning != "early" {
		t.Fatalf("visible = %q reasoning = %q", visible, reasoning)
	}
}

// A retry reset starts the reader over.
func TestReasoningMarkerReaderResetsWithReasoning(t *testing.T) {
	visible, reasoning := readAll(thinkMarkers,
		content("<think>abandoned"), modelcall.StreamChunk{ResetReasoning: true}, content("<think>kept</think>Answer"),
	)
	if visible != "Answer" || reasoning != "abandonedkept" {
		t.Fatalf("visible = %q reasoning = %q", visible, reasoning)
	}
}

// Chunks that carried only a held tag are not forwarded, so first-output
// timing and captures see the separated text.
func TestReasoningMarkerReaderWithholdsTagOnlyChunks(t *testing.T) {
	reader := newReasoningMarkerReader(thinkMarkers)
	if _, keep := reader.chunk(content("<think>")); keep {
		t.Fatal("a chunk holding only the open tag must not be forwarded")
	}
	mapped, keep := reader.chunk(modelcall.StreamChunk{Content: "<think>", Usage: modelcall.TokenUsage{Present: true, PromptTokens: 1}})
	if !keep || mapped.Content != "" || !mapped.Usage.Reported() {
		t.Fatalf("a chunk with usage must be forwarded without the tag: %+v", mapped)
	}
}

func TestSeparateCompletionReasoning(t *testing.T) {
	sideChannel := &modelcall.Completion{Content: "<think>\nAnswer", Reasoning: "plan"}
	separateCompletionReasoning(sideChannel, thinkMarkers)
	if sideChannel.Content != "Answer" || sideChannel.Reasoning != "plan" {
		t.Fatalf("side channel: %+v", sideChannel)
	}
	inline := &modelcall.Completion{Content: "<think>plan</think>\nAnswer"}
	separateCompletionReasoning(inline, thinkMarkers)
	if inline.Content != "Answer" || inline.Reasoning != "plan" {
		t.Fatalf("inline: %+v", inline)
	}
	plain := &modelcall.Completion{Content: "Answer"}
	separateCompletionReasoning(plain, thinkMarkers)
	if plain.Content != "Answer" || plain.Reasoning != "" {
		t.Fatalf("plain: %+v", plain)
	}
	separateCompletionReasoning(nil, thinkMarkers)
}

type markerScriptedProvider struct {
	chunks []modelcall.StreamChunk
}

func (p *markerScriptedProvider) ID() string { return "p" }
func (p *markerScriptedProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "Qwen/Qwen3.8-Flash"}}
}
func (p *markerScriptedProvider) Profile() providerprofile.Profile {
	profile := providerprofile.Together()
	profile.Discovery = providerprofile.DiscoveryNone
	return profile
}
func (p *markerScriptedProvider) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	completion, _, err := modelcall.CollectStream(p.stream())
	return completion, err
}
func (p *markerScriptedProvider) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return p.stream(), nil
}
func (p *markerScriptedProvider) stream() <-chan modelcall.StreamChunk {
	ch := make(chan modelcall.StreamChunk, len(p.chunks))
	for _, c := range p.chunks {
		ch <- c
	}
	close(ch)
	return ch
}

// The registry chain reads think tags for a model no rule names, on both the
// streaming and the complete path.
func TestRegistryChainSeparatesReasoningMarkersForAnyModel(t *testing.T) {
	withThinkingRules(t, nil)
	scripted := &markerScriptedProvider{chunks: []modelcall.StreamChunk{
		content("<think>"), content("\n"), reasoning("plan"),
		{Content: "Answer", Usage: modelcall.TokenUsage{Present: true, PromptTokens: 3, CompletionTokens: 2}, Done: true},
	}}
	snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{Providers: map[string]modelcall.Provider{"p": scripted}})
	reg := newTestRegistry()
	reg.snapshot.Store(snapshot)
	provider := reg.decorateProvider(snapshot, scripted)
	req := modelcall.CompletionRequest{Model: "gemma4-26b", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}}

	stream, err := provider.Stream(t.Context(), req)
	testutil.FailErr(t, "stream", err)
	streamed, _, err := modelcall.CollectStream(stream)
	testutil.FailErr(t, "collect stream", err)
	if streamed.Content != "Answer" || streamed.Reasoning != "plan" || streamed.Usage.CompletionTokens != 2 {
		t.Fatalf("streamed = %+v, want the tag separated and usage kept", streamed)
	}

	completed, err := provider.Complete(t.Context(), req)
	testutil.FailErr(t, "complete", err)
	if completed.Content != "Answer" || completed.Reasoning != "plan" {
		t.Fatalf("completed = %+v", completed)
	}

	// A family declaring its own tags is read with those, not the default.
	other := modelinfo.ReasoningMarkers{Open: "<reasoning>", Close: "</reasoning>"}
	withThinkingRules(t, []modelinfo.ThinkingRule{{Match: []string{"gemma4"}, Style: "none", ReasoningMarkers: &other}})
	scripted.chunks = []modelcall.StreamChunk{{Content: "<reasoning>plan</reasoning>\n<think>Answer", Done: true}}
	declared, err := provider.Complete(t.Context(), req)
	testutil.FailErr(t, "complete with declared markers", err)
	if declared.Content != "<think>Answer" || declared.Reasoning != "plan" {
		t.Fatalf("declared = %+v, want the family's tags read and the default left alone", declared)
	}
}

// An unclosed block that ends with a tool call is reasoning: the call runs
// once, and nothing of the block reaches the transcript to be completed on
// the next call.
func TestReasoningMarkerReaderUnclosedBlockBeforeToolCall(t *testing.T) {
	reader := newReasoningMarkerReader(thinkMarkers)
	first, keep := reader.chunk(content("<think>The user wants the file read."))
	if keep && first.Content != "" {
		t.Fatalf("content leaked: %+v", first)
	}
	call, _ := reader.chunk(modelcall.StreamChunk{ToolCalls: []api.ToolCall{{Name: "command"}}, Done: true})
	if call.Content != "" || first.Reasoning+call.Reasoning != "The user wants the file read." || len(call.ToolCalls) != 1 {
		t.Fatalf("chunks = %+v then %+v, want the block as reasoning beside the call", first, call)
	}
}

// A turn whose whole output was reasoning, cut off at the output cap, is an
// empty completion to the retry layer, which recovers it instead of handing
// the transcript a raw block or nothing.
func TestRegistryChainRetriesReasoningOnlyTurn(t *testing.T) {
	withThinkingRules(t, nil)
	inner := &responseRetryStub{stream: func(call int) (<-chan modelcall.StreamChunk, error) {
		if call == 1 {
			return retryChunkStream(content("<think>"), content("I should answer carefully"), modelcall.StreamChunk{Done: true}), nil
		}
		return retryChunkStream(modelcall.StreamChunk{Content: "Answer", Done: true}), nil
	}}
	snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: map[string]modelcall.Provider{inner.ID(): inner},
		Entries:   map[string]CatalogEntry{inner.ID(): {ID: inner.ID(), HTTPRetry: retryEmptyOncePolicy()}},
	})
	provider := newTestRegistry().decorateProvider(snapshot, inner)

	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}})
	testutil.FailErr(t, "stream", err)
	got, _, err := modelcall.CollectStream(stream)
	testutil.FailErr(t, "collect stream", err)
	if got.Content != "Answer" || inner.streamCalls != 2 {
		t.Fatalf("completion = %+v after %d calls, want the answer on the second attempt", got, inner.streamCalls)
	}
	if strings.Contains(got.Reasoning, "carefully") {
		t.Fatalf("reasoning = %q, want the failed attempt's block dropped", got.Reasoning)
	}
}

func TestValidateThinkingRulesChecksReasoningMarkers(t *testing.T) {
	for name, markers := range map[string]modelinfo.ReasoningMarkers{
		"empty open":     {Close: "</think>"},
		"same tags":      {Open: "<x>", Close: "<x>"},
		"padded tag":     {Open: " <think>", Close: "</think>"},
		"overlong tag":   {Open: strings.Repeat("<", 33), Close: "</think>"},
		"empty close":    {Open: "<think>"},
		"whitespace tag": {Open: "\n", Close: "</think>"},
	} {
		t.Run(name, func(t *testing.T) {
			rules := []modelinfo.ThinkingRule{{Match: []string{"m"}, Style: "none", ReasoningMarkers: &markers}}
			if err := modelinfo.ValidateThinkingRules(rules); err == nil {
				t.Fatal("invalid markers accepted")
			}
		})
	}
	valid := []modelinfo.ThinkingRule{{Match: []string{"m"}, Style: "none", ReasoningMarkers: &thinkMarkers}}
	if err := modelinfo.ValidateThinkingRules(valid); err != nil {
		t.Fatalf("valid markers rejected: %v", err)
	}
}

// Think tags are read for every model under the bundled rules, listed or not.
func TestReasoningMarkersCoverEveryModel(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	for _, model := range []string{"Qwen/Qwen3.8-Flash", "deepseek-r1:8b", "gemma4-26b", "claude-sonnet-5", "a-model-no-rule-names"} {
		if got := modelinfo.ReasoningMarkersFor(model); got != thinkMarkers {
			t.Fatalf("%s: markers = %+v, want the default think tags", model, got)
		}
	}
}
