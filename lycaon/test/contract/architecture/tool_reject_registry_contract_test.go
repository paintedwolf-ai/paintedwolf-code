package contract

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/rejectinventory"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// scanToolRejectEmission inventories the production emission sites. Codes the
// module copies from shipped OAR rules and posture rules come from those
// catalogs' production loaders.
func scanToolRejectEmission(t *testing.T) rejectinventory.Inventory {
	t.Helper()
	inv, err := rejectinventory.Scan(filepath.Join(contractcheck.RepoRoot(t), "lycaon"), rejectinventory.Options{
		Patterns: []string{"./internal/...", "./pkg/..."},
		Decoded: map[string]rejectinventory.Enumerator{
			"github.com/lycaon/lycaon/internal/oarcore.Rule.ID": func() ([]string, error) {
				var ids []string
				for _, rule := range toolfixture.StockOARRules(t).All() {
					ids = append(ids, rule.ID)
				}
				return ids, nil
			},
			"github.com/lycaon/lycaon/internal/rules.RuleOutcome.RejectCode": postureRuleDenyCodes,
			"github.com/lycaon/lycaon/internal/rules.RuleOutcome.Code":       postureRuleDenyCodes,
		},
	})
	contractcheck.FailErr(t, "scan reject emission sites", err)
	return inv
}

// postureRuleDenyCodes lists the deny codes the shipped posture rules declare.
func postureRuleDenyCodes() ([]string, error) {
	configs, err := rules.LoadBundledRuleConfigs()
	if err != nil {
		return nil, err
	}
	var codes []string
	for _, cfg := range configs {
		for _, rule := range cfg.Rules {
			if deny, ok := rule.Then["deny"].(map[string]any); ok {
				if code, ok := deny["code"].(string); ok {
					codes = append(codes, code)
				}
			}
		}
	}
	return codes, nil
}

// Every ToolReject code has stock policy copy.
func TestToolRejectCodesRegisteredInHintRegistry(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)

	missing := map[string][]string{}
	for _, s := range scanToolRejectEmission(t).Sites {
		for _, code := range s.Codes {
			if _, ok := cfg.HintCodes[code]; !ok {
				missing[code] = append(missing[code], s.Rel+":"+strconv.Itoa(s.Line))
			}
		}
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

// Every recognized carrier or forwarder emission, including one whose code is
// dynamic, resolves to finite codes through its upstream typed producer, so the
// registry and copy checks see every code a handler can emit.
func TestToolRejectEmissionCodesResolve(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for _, u := range scanToolRejectEmission(t).Unresolved {
		fmt.Fprintf(&b, "  %s:%d in %s (%s): code %s: %s\n", u.Rel, u.Line, u.Func, u.Kind, u.Expr, u.Reason)
	}
	if b.Len() > 0 {
		t.Fatalf("reject emissions with unresolved codes:\n%s", b.String())
	}
}

// TestToolRejectCodesFormatAsStructuredRejectedBlocks checks rendered refusals.
func TestToolRejectCodesFormatAsStructuredRejectedBlocks(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(cfg)

	seen := map[string]bool{}
	for _, site := range scanToolRejectEmission(t).Sites {
		for _, code := range site.Codes {
			if seen[code] {
				continue
			}
			seen[code] = true
			if _, ok := cfg.HintCodes[code]; ok {
				checkStructuredRejectBlock(t, cfg, formatter, code)
			}
		}
	}
	if len(seen) < 10 {
		t.Fatalf("expected a substantial ToolReject inventory, got %d unique codes", len(seen))
	}
}

func checkStructuredRejectBlock(t *testing.T, cfg *guidance.HintConfig, formatter *guidance.StaticRejectFormatter, code string) {
	t.Helper()
	formatted := toolrejection.FormatDecisionReject(code, map[string]any{
		"tool": "summarize", "path": "README.md", "profile": "coordinator",
		"bytes": 10, "cap": 400000, "floor": 200, "detail": "test",
		"need_one_of": []string{"path", "paths", "pattern", "content"},
	}, formatter)
	if formatted == nil {
		t.Fatalf("%s: FormatDecisionReject returned nil", code)
	}
	msg := formatted.Error()
	if !strings.Contains(msg, "Rejected:") {
		t.Fatalf("%s: formatted reject must contain Rejected:, got %q", code, msg)
	}
	if !strings.Contains(msg, "Code: "+code) {
		t.Fatalf("%s: formatted reject missing Code line:\n%s", code, msg)
	}
	// Refusals include structured copy.
	if strings.TrimSpace(msg) == code {
		t.Fatalf("%s: FormatDecisionReject returned bare code (would wire as completed)", code)
	}

	// Wire metadata comes from the refusal.
	refusal, ok := guidance.RefusalFromError(formatted)
	if !ok {
		t.Fatalf("%s: FormatDecisionReject must return a refusal carrying its code", code)
	}
	if refusal.Code() != code {
		t.Fatalf("%s: refusal code = %q", code, refusal.Code())
	}
	tr := guidance.ComposeToolResult(refusal.Body, refusal.Facts, cfg)
	if tr == nil {
		t.Fatalf("%s: ComposeToolResult returned nil", code)
	}
	if tr.Outcome != api.ToolResultOutcomeRejected {
		t.Fatalf("%s: outcome = %q want rejected", code, tr.Outcome)
	}
	if tr.PrimaryCode() != code {
		t.Fatalf("%s: tool_result.codes = %v", code, tr.Codes)
	}
}

// Missing policy copy does not change refusal metadata.
func TestUnregisteredRejectCodeStillWiresAsRejected(t *testing.T) {
	t.Parallel()
	fallback := toolrejection.FormatDecisionReject("NOT_IN_REGISTRY_INVARIANT_TEST", nil, guidance.NewStaticRejectFormatter(&guidance.HintConfig{HintCodes: map[string]guidance.HintEntry{}}))
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
