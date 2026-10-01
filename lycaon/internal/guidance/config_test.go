package guidance_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestEvidenceMetaDrivesCheckAndRetry(t *testing.T) {
	cfg := &guidance.HintConfig{HintCodes: map[string]guidance.HintEntry{
		"WORKER_EXCERPT_HANDLE_MISMATCH": {Evidence: &guidance.EvidenceMeta{
			UILabel:   "Finding excerpts",
			UICheckID: "finding_excerpts",
		}},
		"WORKER_EVIDENCE_HANDLE_UNKNOWN": {Evidence: &guidance.EvidenceMeta{
			UILabel:        "Finding citations",
			UICheckID:      "finding_citations",
			InSessionRetry: true,
		}},
		"WORKER_SUMMARY_TOO_LONG": {}, // no evidence block
	}}

	label, id, ok := cfg.EvidenceCheckMeta("WORKER_EXCERPT_HANDLE_MISMATCH")
	if !ok || label != "Finding excerpts" || id != "finding_excerpts" {
		t.Fatalf("EvidenceCheckMeta = %q/%q ok=%v", label, id, ok)
	}
	if _, _, ok := cfg.EvidenceCheckMeta("WORKER_SUMMARY_TOO_LONG"); ok {
		t.Fatal("code without evidence block must report ok=false")
	}
	if cfg.IsInSessionRetry("WORKER_EXCERPT_HANDLE_MISMATCH") {
		t.Fatal("traced tier metadata must not be retry-eligible")
	}
	if !cfg.IsInSessionRetry("WORKER_EVIDENCE_HANDLE_UNKNOWN") {
		t.Fatal("declared retry code must be eligible")
	}
	if cfg.IsInSessionRetry("WORKER_SUMMARY_TOO_LONG") {
		t.Fatal("code without evidence block must not be retry-eligible")
	}
	if got := strings.Join(cfg.InSessionRetryCodes(), ","); got != "WORKER_EVIDENCE_HANDLE_UNKNOWN" {
		t.Fatalf("InSessionRetryCodes = %q", got)
	}
}

func TestHintCatalogProjection(t *testing.T) {
	const document = `oar: '1.0'
id: EXAMPLE
status: deprecated
related:
  - id: REPLACEMENT
    type: obsolete
selector:
  tool: [command, verify]
copy:
  title: Review action
  what: Action needs review
  cause: Approval is pending
  why: The action crosses a boundary
  fix: Resolve the approval
  instead: Wait for the approval result
x-paintedwolf-emit: guard:tool
x-paintedwolf-category: recoverable
x-paintedwolf-message: Review action
x-paintedwolf-evidence:
  ui_label: Source
  ui_check_id: source
  in_session_retry: true
x-paintedwolf-context-schema:
  type: object
x-paintedwolf-scenarios:
  - id: default
`
	var entry guidance.HintEntry
	testutil.FailErr(t, "decode catalog rule", yaml.Unmarshal([]byte(document), &entry))
	if entry.Title != "Review action" || entry.What != "Action needs review" ||
		entry.Cause != "Approval is pending" || entry.Why != "The action crosses a boundary" ||
		entry.Fix != "Resolve the approval" || entry.Instead != "Wait for the approval result" {
		t.Fatalf("copy projection = %+v", entry)
	}
	if entry.Emit != "guard:tool" || entry.Category != "recoverable" || entry.Message != "Review action" {
		t.Fatalf("extension projection = %+v", entry)
	}
	if strings.Join(entry.Tools, ",") != "command,verify" || len(entry.Scenarios) != 1 ||
		entry.Evidence == nil || !entry.Evidence.InSessionRetry || entry.ContextSchema["type"] != "object" {
		t.Fatalf("coverage projection = %+v", entry)
	}
	if entry.Status != "deprecated" || len(entry.Related) != 1 || entry.Related[0].ID != "REPLACEMENT" {
		t.Fatalf("lineage = %s %v", entry.Status, entry.Related)
	}
}

func TestPlainHintCatalogFields(t *testing.T) {
	const document = `id: EXAMPLE
category: recoverable
severity: error
message: Review action
what: Action needs review
cause: Approval is pending
fix: Resolve the approval
instead: Wait for the approval result
tools: [command]
scenarios:
  - id: default
`
	var entry guidance.HintEntry
	testutil.FailErr(t, "decode display hint", yaml.Unmarshal([]byte(document), &entry))
	if entry.OAR != "" || entry.Category != "recoverable" || entry.Severity != "error" ||
		entry.Message != "Review action" || entry.What != "Action needs review" ||
		entry.Cause != "Approval is pending" || entry.Fix != "Resolve the approval" ||
		entry.Instead != "Wait for the approval result" || strings.Join(entry.Tools, ",") != "command" ||
		len(entry.Scenarios) != 1 {
		t.Fatalf("display hint = %+v", entry)
	}
}
