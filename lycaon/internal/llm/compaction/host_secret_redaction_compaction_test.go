package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionCompactionCarriesHostRedactionOntoCheckpoint(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	cfg.KeepRecentMessages = 1
	cfg.ChunkTokenThreshold = 1_000_000
	msgs := []ContextMessage{
		{
			ID: "redacted", Role: string(api.MessageRoleTool),
			Content:             strings.Repeat("exposed credential [REDACTED]\n", 200),
			HostSecretRedaction: api.NewHostSecretRedactionMeta([]api.RedactedSpan{{Field: "content", Start: 0, Length: 10, Kind: api.RedactionKindSecret}}),
		},
		{ID: "tail", Role: string(api.MessageRoleAssistant), Content: strings.Repeat("continue\n", 200)},
	}
	c := NewSimpleCompactor(cfg, MockSummarizer{Text: "credential remediation remains open"})
	out, report, err := c.Compact(context.Background(), SessionInfo{ID: "s1"}, msgs, 20)
	testutil.FailErr(t, "compact redacted history", err)
	if !report.SessionCompacted || len(out) < 2 {
		t.Fatalf("compaction report/output = %+v %+v", report, out)
	}
	if out[0].HostSecretRedaction == nil || out[0].HostSecretRedaction.Occurrences() != 1 {
		t.Fatalf("checkpoint redaction provenance = %+v", out[0].HostSecretRedaction)
	}
}

func TestCompactionPromptLabelsHostRedactionOutsideContent(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	input, err := BuildCompactionInput(context.Background(), SessionInfo{ID: "s1"}, []ContextMessage{{
		Role: string(api.MessageRoleTool), Content: "token=[REDACTED]",
		HostSecretRedaction: api.NewHostSecretRedactionMeta([]api.RedactedSpan{{Field: "content", Start: 0, Length: 10, Kind: api.RedactionKindSecret}}),
	}}, 8192, 512)
	testutil.FailErr(t, "build compaction prompt", err)
	prompt := input.Text
	if !strings.Contains(prompt, "[host redacted secret values; sources unchanged by redaction]") {
		t.Fatalf("compaction prompt lost host provenance: %q", prompt)
	}
	if strings.Contains(prompt, "host_secret_references=true") {
		t.Fatalf("a removal was labeled as a reference: %q", prompt)
	}
}

func TestCompactionPromptLabelsHostReferencesApartFromRedaction(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	input, err := BuildCompactionInput(context.Background(), SessionInfo{ID: "s1"}, []ContextMessage{{
		Role: string(api.MessageRoleTool), Content: "token={{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}}",
		HostSecretRedaction: api.NewHostSecretRedactionMeta([]api.RedactedSpan{{
			Field: "content", Start: 6, Length: 59, Kind: api.RedactionKindManagedReference,
		}}),
	}}, 8192, 512)
	testutil.FailErr(t, "build compaction prompt", err)
	if !strings.Contains(input.Text, "host_secret_references=true") || strings.Contains(input.Text, "host_secret_redaction=true") {
		t.Fatalf("compaction prompt mislabeled a reference: %q", input.Text)
	}
}

func TestCompactionPromptKeepsModelHistoryFreeOfTransportLabels(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	input, err := BuildCompactionInput(context.Background(), SessionInfo{ID: "s1"}, []ContextMessage{{
		Role: string(api.MessageRoleAssistant), Content: "I inspected the repository.",
	}}, 8192, 512)
	testutil.FailErr(t, "build compaction prompt", err)
	prompt := input.Text
	if !strings.Contains(prompt, "I inspected the repository.") || strings.Contains(prompt, "⟦D:model⟧") {
		t.Fatalf("compaction prompt leaked model transport metadata: %q", prompt)
	}
}
