package tooloutput_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

func TestIsAgentWireSpillRel(t *testing.T) {
	ok := []string{
		"tool-output/a.txt",
		"tool-output/notes..draft.txt",
		"promote-spills/job-1.json",
		"prompt-attachments/uuid-name.txt",
	}
	for _, p := range ok {
		if !tooloutput.IsAgentWireSpillRel(p) {
			t.Fatalf("expected allowlisted %q", p)
		}
	}
	bad := []string{
		"",
		"tool-output",
		"tool-output/",
		"tool-output/../x",
		"../tool-output/a.txt",
		"/tmp/host/tool-output/a.txt",
		settingsoverlay.Rel("host/tool-output/a.txt"),
		"src/main.go",
		"credential-vault.age",
		"scan-results/id.json",
		"evidence/x.jsonl",
	}
	for _, p := range bad {
		if tooloutput.IsAgentWireSpillRel(p) {
			t.Fatalf("expected reject %q", p)
		}
	}
}

func TestAgentWireSpillScopeRelSeatbelt(t *testing.T) {
	host := t.TempDir()
	spillAbs := filepath.Join(host, "tool-output", "a.txt")
	scope, ok := tooloutput.AgentWireSpillScopeRel(host, spillAbs)
	if !ok || scope != "tool-output/a.txt" {
		t.Fatalf("scope=%q ok=%v", scope, ok)
	}
	evidenceAbs := filepath.Join(host, "evidence", "x.jsonl")
	if _, ok := tooloutput.AgentWireSpillScopeRel(host, evidenceAbs); ok {
		t.Fatal("evidence under host must not admit")
	}
}

func TestIsAgentWireSpillReadArg(t *testing.T) {
	if !tooloutput.IsAgentWireSpillReadArg("promote-spills/job-a.json") {
		t.Fatal("relative wire")
	}
	if !tooloutput.IsAgentWireSpillReadArg("/tmp/host/promote-spills/job-a.json") {
		t.Fatal("absolute leftover seatbelt")
	}
	if tooloutput.IsAgentWireSpillReadArg("/tmp/host/evidence/x.jsonl") {
		t.Fatal("non-spill abs")
	}
}
