package workercompletion_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSynthesizeSummaryFromChildMessages(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleTool, Content: strings.Repeat("a", 30)},
		{Role: api.MessageRoleTool, Content: strings.Repeat("b", 30)},
	}
	out := workercompletion.SynthesizeSummaryFromChildMessages(msgs, "path-explorer")
	if !strings.Contains(out, "Synthesized survey") || !strings.Contains(out, "Tool excerpts") {
		t.Fatalf("out = %q", out)
	}
}

func TestSynthesizeSummaryFromChildMessagesEmpty(t *testing.T) {
	if workercompletion.SynthesizeSummaryFromChildMessages(nil, "path-explorer") != "" {
		t.Fatal("expected empty")
	}
}
