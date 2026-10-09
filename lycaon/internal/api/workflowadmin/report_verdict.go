package workflowadmin

import (
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/report"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
)

// projectVerdicts writes the phases' records in order, and returns beside
// them the citation channels each record carried, keyed by phase.
func projectVerdicts(records []workflowpresentation.PhaseVerdict) ([]report.ReportVerdict, map[string][]citation, map[string][]string) {
	var out []report.ReportVerdict
	channels := map[string][]citation{}
	urls := map[string][]string{}
	for _, r := range records {
		v := reportVerdict(r.Record, r.Def)
		if v == nil {
			continue
		}
		v.Phase = r.Phase
		v.ReconcilesPhase = r.Def.ReconcilesPhase
		v.Label = strings.TrimSpace(r.Label)
		v.RecordedAt = recordedAt(r.Record)
		out = append(out, *v)
		channels[r.Phase] = channelCitations(r.Phase, r.Record.Artifacts)
		urls[r.Phase] = artifactStrings(r.Record.Artifacts, evidence.CitedURLsArtifactKey)
	}
	return out, channels, urls
}

// recordedAt normalises a gate record's stamp to RFC 3339.
func recordedAt(rec evidence.Record) string {
	at := rec.GateAt()
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// channelCitations reads the verdict's own cited_evidence channel, which the
// record stores as a list of maps.
func channelCitations(phase string, arts map[string]any) []citation {
	raw, _ := arts[evidence.CitedEvidenceArtifactKey].([]any)
	var out []citation
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		c := citation{by: phase}
		c.handle, _ = m["handle"].(string)
		c.path, _ = m["path"].(string)
		c.line = intValue(m["line"])
		c.handle, c.path = strings.TrimSpace(c.handle), strings.TrimSpace(c.path)
		if c.handle == "" && c.path == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func intValue(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	default:
		return 0
	}
}

// artifactStrings reads a list-of-strings artifact.
func artifactStrings(arts map[string]any, key string) []string {
	var out []string
	switch raw := arts[key].(type) {
	case []any:
		for _, item := range raw {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case []string:
		for _, s := range raw {
			if strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

// reportVerdict projects a stamped record through the phase's own verdict
// schema. Nothing here names a schema member, so any schema reports the same
// way; only the reserved decision and citation channels are treated apart.
func reportVerdict(rec evidence.Record, def workflowdef.ReviewLoopDef) *report.ReportVerdict {
	members := workflowpresentation.VerdictMembers(rec.Artifacts)
	decision := strings.TrimSpace(members[workflowdef.VerdictDecisionKey])
	if decision == "" {
		decision = strings.TrimSpace(rec.Summary)
	}
	if decision == "" {
		return nil
	}

	out := &report.ReportVerdict{Decision: decision}

	// A claims-typed member is rendered as claims, not repeated as a field.
	claimed := map[string]bool{}
	byField, err := workflowvalidation.ParseVerdictClaims(def, members)
	if err == nil {
		names := make([]string, 0, len(byField))
		for name := range byField {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			claimed[name] = true
			for _, c := range byField[name] {
				out.Claims = append(out.Claims, reportClaim(c))
			}
		}
	}

	// A set-aside member is accounted in the scanner inventory, not repeated.
	for field, kind := range def.VerdictSchema {
		if strings.TrimSpace(kind) == workflowdef.VerdictSetAsidesType || strings.TrimSpace(kind) == workflowdef.VerdictCoverageType {
			claimed[field] = true
		}
	}
	for _, f := range workflowreview.OrderVerdictFields(members) {
		if f.Name == workflowdef.VerdictDecisionKey || claimed[f.Name] || strings.TrimSpace(f.Value) == "" {
			continue
		}
		out.Fields = append(out.Fields, report.ReportVerdictField{Name: f.Name, Value: f.Value})
	}
	return out
}

func reportClaim(c workflowvalidation.VerdictClaim) report.ReportVerdictClaim {
	out := report.ReportVerdictClaim{
		ScanGroupIDs: append([]string(nil), c.ScanGroupIDs...),
		ID:           strings.TrimSpace(c.ID),
		Title:        strings.TrimSpace(c.Title),
		Statement:    strings.TrimSpace(c.Statement),
		Status:       strings.TrimSpace(c.Status),
	}
	for _, cite := range c.CitedEvidence {
		handle, path := strings.TrimSpace(cite.Handle), strings.TrimSpace(cite.Path)
		if handle == "" && path == "" {
			continue
		}
		out.CitedEvidence = append(out.CitedEvidence, report.ReportClaimCitation{Handle: handle, Path: path, Line: cite.Line})
	}
	return out
}
