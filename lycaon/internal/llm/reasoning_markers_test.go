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

// The registry chain applies the family's markers to every transport, on
// both the streaming and the complete path.
func TestRegistryChainSeparatesDeclaredReasoningMarkers(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{
		{Match: []string{"qwen3"}, Style: string(modelinfo.ThinkStyleBooleanThink), ReasoningMarkers: &thinkMarkers},
	})
	scripted := &markerScriptedProvider{chunks: []modelcall.StreamChunk{
		content("<think>"), content("\n"), reasoning("plan"),
		{Content: "Answer", Usage: modelcall.TokenUsage{Present: true, PromptTokens: 3, CompletionTokens: 2}, Done: true},
	}}
	snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{Providers: map[string]modelcall.Provider{"p": scripted}})
	reg := newEmptyRegistry()
	reg.snapshot.Store(snapshot)
	provider := reg.decorateProvider(snapshot, scripted)
	req := modelcall.CompletionRequest{Model: "Qwen/Qwen3.8-Flash", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}}

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

	// A family with no markers passes content through untouched.
	withThinkingRules(t, []modelinfo.ThinkingRule{{Match: []string{"qwen3"}, Style: string(modelinfo.ThinkStyleBooleanThink)}})
	untouched, err := provider.Complete(t.Context(), req)
	testutil.FailErr(t, "complete without markers", err)
	if untouched.Content != "<think>\nAnswer" {
		t.Fatalf("content = %q, want the transport's bytes without a declaration", untouched.Content)
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

// The bundled rules declare the tag families whose stacks leak tags.
func TestBundledRulesDeclareThinkTagFamilies(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	for _, model := range []string{"Qwen/Qwen3.8-Flash", "deepseek-r1:8b", "qwen3-30b-a3b-thinking-2507"} {
		rule, ok := modelinfo.MatchThinkingRule(model)
		if !ok || rule.ReasoningMarkers == nil || *rule.ReasoningMarkers != thinkMarkers {
			t.Fatalf("%s: rule = %+v, want <think> markers", model, rule)
		}
	}
	if rule, ok := modelinfo.MatchThinkingRule("claude-sonnet-5"); ok && rule.ReasoningMarkers != nil {
		t.Fatalf("claude rule declares markers: %+v", rule)
	}
}
