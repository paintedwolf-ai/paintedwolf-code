package compaction_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTranscriptDietForToolSummarize(t *testing.T) {
	b := evidence.ActiveBinding()
	if got := b.TranscriptDietForTool("summarize"); got != evidence.TranscriptDietAnchorResidue {
		t.Fatalf("summarize diet = %q want anchor_residue", got)
	}
}

func TestClassifyHotToolResult(t *testing.T) {
	msgs := []compaction.ContextMessage{
		{Role: string(api.MessageRoleUser), Content: "go"},
		{Role: string(api.MessageRoleAssistant), Content: "call", ToolCalls: []api.ToolCall{{ID: "tc1", Name: "command"}}},
		{Role: string(api.MessageRoleTool), ToolCallID: "tc1", ToolName: "command", Content: strings.Repeat("x", 8000)},
	}
	class := compaction.ClassifyMessageDiet(compaction.ClassifyMessageDietInput{
		Index:    2,
		Messages: msgs,
		Binding:  evidence.ActiveBinding(),
	})
	if class.Eligibility != compaction.DietEligibilityHot {
		t.Fatalf("eligibility = %q want hot", class.Eligibility)
	}
	if !class.SkipOversizedChunk(msgs[2].Content) {
		t.Fatal("hot tool result must skip oversized chunk")
	}
}

func TestShapeBoundedReadUnderCeiling(t *testing.T) {
	page := `{"mode":"content","path":"game.py","content":"short","receipt":{"tool":"read"}}`
	msgs := []compaction.ContextMessage{
		{Role: string(api.MessageRoleUser), Content: "go"},
		{Role: string(api.MessageRoleAssistant), Content: "read"},
		{Role: string(api.MessageRoleTool), ToolName: "read", Content: page},
		{Role: string(api.MessageRoleAssistant), Content: "next"},
	}
	class := compaction.ClassifyMessageDiet(compaction.ClassifyMessageDietInput{
		Index:    2,
		Messages: msgs,
		Binding:  evidence.ActiveBinding(),
	})
	if class.Strategy != evidence.TranscriptDietShapeBounded {
		t.Fatalf("strategy = %q want shape_bounded", class.Strategy)
	}
	if !class.SkipOversizedChunk(page) {
		t.Fatal("shape_bounded under ceiling must skip oversized chunk")
	}
}

func TestAnchorResidueSummarizeUnderWireCeiling(t *testing.T) {
	// Over ordinary chunk_token_threshold class sizes, under pack.wire_budget_tokens.
	substance := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		substance = append(substance, `{"path":"a.go","body":"`+strings.Repeat("zzzzzzzzzz", 80)+`"}`)
	}
	pack := `{"task":"t","pack":{"identity":[{"path":"a.go"}],"substance":[` +
		strings.Join(substance, ",") +
		`]},"anchors":[{"handle":"summarize#1"}],"selected":1,"total":1}`
	in := "[summarize#1]\n" + pack
	tokens := tokenest.EstimateDefault(in)
	if tokens < 800 {
		t.Fatalf("fixture too small vs chunk threshold: tokens=%d", tokens)
	}
	if tokens > 2400 {
		t.Fatalf("fixture must stay under wire budget: tokens=%d", tokens)
	}
	msgs := []compaction.ContextMessage{
		{Role: string(api.MessageRoleUser), Content: "go"},
		{Role: string(api.MessageRoleAssistant), Content: "sum"},
		{Role: string(api.MessageRoleTool), ToolName: "summarize", Content: in},
		{Role: string(api.MessageRoleAssistant), Content: "next"},
	}
	class := compaction.ClassifyMessageDiet(compaction.ClassifyMessageDietInput{
		Index:         2,
		Messages:      msgs,
		ForceEligible: true,
		Binding:       evidence.ActiveBinding(),
	})
	if class.Strategy != evidence.TranscriptDietAnchorResidue {
		t.Fatalf("strategy = %q want anchor_residue", class.Strategy)
	}
	if !class.SkipOversizedChunk(in) {
		t.Fatal("wire-fitted summarize must skip oversized chunk / commit spill")
	}
}

func TestScanSpillPointer(t *testing.T) {
	b := evidence.ActiveBinding()
	if got := b.TranscriptDietForTool("scan_pack"); got != evidence.TranscriptDietSpillPointer {
		t.Fatalf("scan_pack diet = %q want spill_pointer", got)
	}
	msgs := []compaction.ContextMessage{
		{Role: string(api.MessageRoleUser), Content: "go"},
		{Role: string(api.MessageRoleAssistant), Content: "scan"},
		{Role: string(api.MessageRoleTool), ToolName: "scan_pack", Content: strings.Repeat("y", 9000)},
		{Role: string(api.MessageRoleAssistant), Content: "next"},
	}
	class := compaction.ClassifyMessageDiet(compaction.ClassifyMessageDietInput{
		Index:    2,
		Messages: msgs,
		Binding:  b,
	})
	if class.Strategy != evidence.TranscriptDietSpillPointer {
		t.Fatalf("strategy = %q want spill_pointer", class.Strategy)
	}
}

func TestWorkerChildEligibleForDiet(t *testing.T) {
	aged := strings.Repeat("z", 9000)
	msgs := []compaction.ContextMessage{
		{Role: string(api.MessageRoleUser), Content: "survey"},
		{Role: string(api.MessageRoleAssistant), Content: "call", ToolCalls: []api.ToolCall{{ID: "tc1", Name: "command"}}},
		{Role: string(api.MessageRoleTool), ToolCallID: "tc1", ToolName: "command", Content: aged},
		{Role: string(api.MessageRoleAssistant), Content: "next"},
	}
	class := compaction.ClassifyMessageDiet(compaction.ClassifyMessageDietInput{
		Index:    2,
		Messages: msgs,
		Binding:  evidence.ActiveBinding(),
	})
	if class.Eligibility != compaction.DietEligibilityEligible {
		t.Fatalf("eligibility = %q want eligible (worker children share coordinator diet)", class.Eligibility)
	}
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.ChunkTokenThreshold = 100
	c := compaction.NewSimpleCompactor(cfg, nil)
	out, n := c.CompactOversizedChunksOnly(t.Context(), compaction.SessionInfo{}, msgs)
	if n == 0 {
		t.Fatal("aged oversized tool result must compact for worker-shaped history")
	}
	if out[2].Content == aged {
		t.Fatal("aged tool content must shrink after chunk compact")
	}
}

func TestNewToolInheritsKindDiet(t *testing.T) {
	b := evidence.ActiveBinding()
	const tool = "mcp_inherit_summarize_diet"
	if err := b.ReplaceMCPToolDeclarations([]evidence.MCPToolDeclaration{{
		ToolName: tool, Kind: "summarize", Shape: "file_region",
	}}); err != nil {
		t.Fatalf("ReplaceMCPToolDeclarations: %v", err)
	}
	if got := b.TranscriptDietForTool(tool); got != evidence.TranscriptDietAnchorResidue {
		t.Fatalf("new tool bound to summarize kind diet = %q want anchor_residue", got)
	}
}
