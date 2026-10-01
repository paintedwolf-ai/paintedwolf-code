package scan

import (
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/scan/hints"
	"github.com/lycaon/lycaon/pkg/api"
)

// ScanHintResolver maps findings to ScanGuidanceSummary using scan-hints.yaml.
type ScanHintResolver struct {
	cfg *hints.Config
}

// NewScanHintResolver constructs a resolver from loaded config.
func NewScanHintResolver(cfg *hints.Config) *ScanHintResolver {
	return &ScanHintResolver{cfg: cfg}
}

// Resolve converts budgeted findings into agent-safe guidance summaries.
func (r *ScanHintResolver) Resolve(findings []api.SecurityFinding) []api.ScanGuidanceSummary {
	if r == nil || r.cfg == nil {
		return nil
	}
	out := make([]api.ScanGuidanceSummary, 0, len(findings))
	for _, f := range findings {
		code, entry := r.resolveFinding(f)
		msg := entry.Message
		if strings.TrimSpace(f.Message) != "" {
			msg = runeclamp.Clamp(f.Message, 200)
		}
		out = append(out, api.ScanGuidanceSummary{
			Code:     code,
			Message:  msg,
			Fix:      entry.Fix,
			Severity: normalizeFindingSeverity(entry.Severity, scanfindings.LevelToGuidanceSeverity(f.Level)),
			RuleID:   f.RuleID,
			File:     scanfindings.PrimaryURI(f),
			Line:     scanfindings.PrimaryLine(f),
		})
	}
	return clusterGuidance(out)
}

// ResolveWithHintCodes returns guidance and findings stamped with properties.lycaon.hint_code.
func (r *ScanHintResolver) ResolveWithHintCodes(findings []api.SecurityFinding) ([]api.ScanGuidanceSummary, []api.SecurityFinding) {
	if r == nil || r.cfg == nil {
		return nil, findings
	}
	stamped := make([]api.SecurityFinding, 0, len(findings))
	out := make([]api.ScanGuidanceSummary, 0, len(findings))
	for _, f := range findings {
		code, entry := r.resolveFinding(f)
		stamped = append(stamped, scanfindings.SetHintCode(f, code))
		msg := entry.Message
		if strings.TrimSpace(f.Message) != "" {
			msg = runeclamp.Clamp(f.Message, 200)
		}
		out = append(out, api.ScanGuidanceSummary{
			Code:     code,
			Message:  msg,
			Fix:      entry.Fix,
			Severity: normalizeFindingSeverity(entry.Severity, scanfindings.LevelToGuidanceSeverity(f.Level)),
			RuleID:   f.RuleID,
			File:     scanfindings.PrimaryURI(f),
			Line:     scanfindings.PrimaryLine(f),
		})
	}
	return clusterGuidance(out), stamped
}

func (r *ScanHintResolver) resolveFinding(f api.SecurityFinding) (string, hints.HintEntry) {
	return r.resolveRule(f.RuleID, scanfindings.FindingKind(f))
}

func (r *ScanHintResolver) resolveRule(ruleID string, kind api.FindingKind) (string, hints.HintEntry) {
	id := strings.TrimSpace(ruleID)
	if id == "" {
		return r.cfg.DefaultHint, r.hintFor(r.cfg.DefaultHint)
	}
	if code, ok := r.cfg.RuleHints[id]; ok {
		return code, r.hintFor(code)
	}
	if prefix := r.longestNamespaceMatch(id); prefix != "" {
		code := r.cfg.RuleHints[prefix]
		return code, r.hintFor(code)
	}
	if kind != "" && kind != api.FindingKindCustom {
		kindKey := "kind:" + string(kind)
		if code, ok := r.cfg.RuleHints[kindKey]; ok {
			return code, r.hintFor(code)
		}
	}
	return r.cfg.DefaultHint, r.hintFor(r.cfg.DefaultHint)
}

// longestNamespaceMatch returns the longest colon-terminated prefix.
// Exact keys are excluded, and length makes map order irrelevant.
func (r *ScanHintResolver) longestNamespaceMatch(id string) string {
	best := ""
	for prefix := range r.cfg.RuleHints {
		if !strings.HasSuffix(prefix, ":") || !strings.HasPrefix(id, prefix) {
			continue
		}
		if len(prefix) > len(best) {
			best = prefix
		}
	}
	return best
}

func (r *ScanHintResolver) hintFor(code string) hints.HintEntry {
	if entry, ok := r.cfg.Hints[code]; ok {
		return entry
	}
	return hints.HintEntry{
		Message:  "Security finding",
		Severity: "warning",
	}
}

func normalizeFindingSeverity(hintSev, findingSev string) string {
	if s := strings.ToLower(strings.TrimSpace(hintSev)); s != "" {
		return s
	}
	switch strings.ToLower(strings.TrimSpace(findingSev)) {
	case "error":
		return "error"
	case "warning", "warn":
		return "warning"
	default:
		return "info"
	}
}

func clusterGuidance(in []api.ScanGuidanceSummary) []api.ScanGuidanceSummary {
	type key struct {
		code string
		file string
	}
	counts := map[key]int{}
	order := make([]key, 0, len(in))
	byKey := map[key]api.ScanGuidanceSummary{}
	for _, g := range in {
		k := key{code: g.Code, file: g.File}
		if _, ok := counts[k]; !ok {
			order = append(order, k)
			byKey[k] = g
		}
		counts[k]++
	}
	out := make([]api.ScanGuidanceSummary, 0, len(order))
	for _, k := range order {
		g := byKey[k]
		g.Count = counts[k]
		out = append(out, g)
	}
	return out
}
