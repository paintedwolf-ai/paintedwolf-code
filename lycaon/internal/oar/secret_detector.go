package oar

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// SecretMatchDetector produces redaction-safe secret_matches facts.
type SecretMatchDetector struct {
	Matcher *secretmatch.Matcher
}

// Name implements Detector.
func (SecretMatchDetector) Name() string { return "secretmatch" }

// Inspect screens content and returns one secret_matches finding.
func (d SecretMatchDetector) Inspect(gc *GuardContext) ([]Finding, error) {
	if d.Matcher == nil {
		return nil, fmt.Errorf("[OAR-FACT-26] secret_matches provider unavailable")
	}
	if gc == nil || d.Matcher.Inert() {
		return []Finding{{Fact: "secret_matches", Value: []any{}}}, nil
	}
	ctx := secretmatch.WithAskAttribution(context.Background(), secretmatch.AskAttribution{ProjectID: gc.ProjectID, SessionID: gc.SessionID})
	hits := d.Matcher.ScreenContext(ctx, gc.Content)
	vals := make([]any, 0, len(hits))
	for _, m := range hits {
		vals = append(vals, map[string]any{
			"start":   m.Start,
			"end":     m.End,
			"rule_id": m.RuleID,
			"shape":   m.GenericShape,
		})
	}
	return []Finding{{Fact: "secret_matches", Value: vals}}, nil
}
