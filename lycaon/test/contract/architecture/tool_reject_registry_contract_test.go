package contract

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every ToolReject code has stock policy copy.
func TestToolRejectCodesRegisteredInHintRegistry(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)

	sites, err := scanToolRejectCodes(lycaonRoot)
	contractcheck.FailErr(t, "scan ToolReject sites", err)
	if len(sites) == 0 {
		t.Fatal("scan found zero ToolReject sites — AST walker is broken")
	}

	missing := map[string][]string{}
	for _, s := range sites {
		if _, ok := cfg.HintCodes[s.Code]; ok {
			continue
		}
		missing[s.Code] = append(missing[s.Code], s.Rel+":"+strconv.Itoa(s.Line))
	}
	if len(missing) == 0 {
		return
	}
	codes := make([]string, 0, len(missing))
	for code := range missing {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	var b strings.Builder
	b.WriteString("ToolReject codes missing from stock pack policy/ (add YAML under packs/*/policy/):\n")
	for _, code := range codes {
		b.WriteString("  ")
		b.WriteString(code)
		b.WriteString(" @ ")
		b.WriteString(strings.Join(missing[code], ", "))
		b.WriteByte('\n')
	}
	t.Fatal(b.String())
}

// TestToolRejectCodesFormatAsStructuredRejectedBlocks checks rendered refusals.
func TestToolRejectCodesFormatAsStructuredRejectedBlocks(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(cfg)

	sites, err := scanToolRejectCodes(lycaonRoot)
	contractcheck.FailErr(t, "scan ToolReject sites", err)

	seen := map[string]bool{}
	for _, s := range sites {
		if seen[s.Code] {
			continue
		}
		seen[s.Code] = true
		if _, ok := cfg.HintCodes[s.Code]; !ok {
			// Covered by TestToolRejectCodesRegisteredInHintRegistry.
			continue
		}
		formatted := tools.FormatDecisionReject(s.Code, map[string]any{
			"tool": "summarize", "path": "README.md", "profile": "coordinator",
			"bytes": 10, "cap": 400000, "floor": 200, "detail": "test",
			"need_one_of": []string{"path", "paths", "pattern", "content"},
		}, formatter)
		if formatted == nil {
			t.Fatalf("%s: FormatDecisionReject returned nil", s.Code)
		}
		msg := formatted.Error()
		if !strings.Contains(msg, "Rejected:") {
			t.Fatalf("%s: formatted reject must contain Rejected:, got %q", s.Code, msg)
		}
		if !strings.Contains(msg, "Code: "+s.Code) {
			t.Fatalf("%s: formatted reject missing Code line:\n%s", s.Code, msg)
		}
		// Refusals include structured copy.
		if strings.TrimSpace(msg) == s.Code {
			t.Fatalf("%s: FormatDecisionReject returned bare code (would wire as completed)", s.Code)
		}

		// Wire metadata comes from the refusal.
		refusal, ok := guidance.RefusalFromError(formatted)
		if !ok {
			t.Fatalf("%s: FormatDecisionReject must return a refusal carrying its code", s.Code)
		}
		if refusal.Code() != s.Code {
			t.Fatalf("%s: refusal code = %q", s.Code, refusal.Code())
		}
		tr := guidance.ComposeToolResult(refusal.Body, refusal.Facts, cfg)
		if tr == nil {
			t.Fatalf("%s: ComposeToolResult returned nil", s.Code)
		}
		if tr.Outcome != api.ToolResultOutcomeRejected {
			t.Fatalf("%s: outcome = %q want rejected", s.Code, tr.Outcome)
		}
		if tr.PrimaryCode() != s.Code {
			t.Fatalf("%s: tool_result.codes = %v", s.Code, tr.Codes)
		}
	}
	if len(seen) < 10 {
		t.Fatalf("expected a substantial ToolReject inventory, got %d unique codes", len(seen))
	}
}

// Missing policy copy does not change refusal metadata.
func TestUnregisteredRejectCodeStillWiresAsRejected(t *testing.T) {
	t.Parallel()
	fallback := tools.FormatDecisionReject("NOT_IN_REGISTRY_INVARIANT_TEST", nil, guidance.NewStaticRejectFormatter(&guidance.HintConfig{HintCodes: map[string]guidance.HintEntry{}}))
	refusal, ok := guidance.RefusalFromError(fallback)
	if !ok {
		t.Fatalf("missing-hint fallback must still be a refusal, got %T", fallback)
	}
	if !strings.HasPrefix(refusal.Body, "Rejected:") || !strings.Contains(refusal.Body, "Code: NOT_IN_REGISTRY_INVARIANT_TEST") {
		t.Fatalf("missing-hint fallback must still be structured, got %q", refusal.Body)
	}
	tr := guidance.ComposeToolResult(refusal.Body, refusal.Facts, nil)
	if tr.Outcome != api.ToolResultOutcomeRejected {
		t.Fatalf("fallback outcome = %q want rejected", tr.Outcome)
	}
	if tr.PrimaryCode() != "NOT_IN_REGISTRY_INVARIANT_TEST" {
		t.Fatalf("fallback codes = %v", tr.Codes)
	}
}
