package syntaxhealth

import (
	"context"
	"testing"
)

func TestExplicitOverrideBypassesOnlySyntaxDecision(t *testing.T) {
	src := []byte("package broken\nfunc (")
	ctx := WithOverride(context.Background(), "intentional incomplete edit")
	for _, change := range []Change{
		Evaluate(ctx, "a.go", nil, false, src),
		EvaluateFinal(ctx, "a.go", src),
	} {
		if change.Transition != TransitionAllowed || change.After.Status != StatusOverridden {
			t.Fatalf("explicit override was not distinguished: %+v", change)
		}
	}
	plain := EvaluateFinal(context.Background(), "a.go", src)
	if plain.Transition != TransitionFinalBroken {
		t.Fatalf("ordinary syntax boundary changed: %+v", plain)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, path := range []string{"a.go", "unknown.extension"} {
		for _, change := range []Change{
			Evaluate(canceled, path, nil, false, src),
			EvaluateFinal(canceled, path, src),
		} {
			if change.Transition != TransitionParseIncomplete || change.After.Failure == nil || change.After.Failure.Reason != "canceled" {
				t.Fatalf("override bypassed cancellation for %s: %+v", path, change)
			}
		}
	}
}
