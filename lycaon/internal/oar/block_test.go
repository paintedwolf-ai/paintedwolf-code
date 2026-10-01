package oar

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEvaluateBlockSchemaObservation(t *testing.T) {
	ensureCatalog(t)
	l, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	rs, err := l.LoadDir(hintsDir(t))
	testutil.FailErr(t, "load", err)
	p := NewGuardPipeline(rs, l, NewCounterStore())
	p.EnableAnchor(AnchorToolRejected)

	gc := NewGuardContext()
	gc.Tool = "grep"
	gc.ObservedRejectCode = "GREP_REGEX_INVALID"
	gc.ArgValidationErrors = []string{"regex_invalid"}
	gc.PutRejectData("GREP_REGEX_INVALID", map[string]any{"detail": "bad", "pattern": "["})
	res, err := p.EvaluateBlock(context.Background(), AnchorToolRejected, gc)
	testutil.FailErr(t, "EvaluateBlock", err)
	if !res.Enforced || res.Decision == nil {
		t.Fatalf("want 1 enforced decision, got %#v", res)
	}
	if res.Decision.Code != "GREP_REGEX_INVALID" {
		t.Fatalf("got %s", res.Decision.Code)
	}
	if res.Decision.Data["detail"] != "bad" {
		t.Fatalf("data=%#v", res.Decision.Data)
	}
}

// Rejection data is attached before rule evaluation.
func TestEvaluateBlockRejectDataBeforeEval(t *testing.T) {
	ensureCatalog(t)
	l, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	rs, err := l.LoadDir(hintsDir(t))
	testutil.FailErr(t, "load", err)
	p := NewGuardPipeline(rs, l, NewCounterStore())
	p.EnableAnchor(AnchorToolRejected)

	gc := NewGuardContext()
	gc.Tool = "read"
	gc.ObservedRejectCode = "READ_PATH_NOT_FOUND"
	gc.NotFound = true
	gc.PutRejectData("READ_PATH_NOT_FOUND", map[string]any{"path": "weather_cli/cli.py"})
	res, err := p.EvaluateBlock(context.Background(), AnchorToolRejected, gc)
	testutil.FailErr(t, "EvaluateBlock", err)
	if !res.Enforced || res.Decision == nil {
		t.Fatalf("want 1 enforced decision, got %#v", res)
	}
	if res.Decision.Code != "READ_PATH_NOT_FOUND" {
		t.Fatalf("got %s", res.Decision.Code)
	}
	if res.Decision.Data["path"] != "weather_cli/cli.py" {
		t.Fatalf("Decision.Data must carry path; got %#v", res.Decision.Data)
	}
	if res.Decision.Data["tool"] != "read" {
		t.Fatalf("Decision.Data must merge tool; got %#v", res.Decision.Data)
	}
}

func TestEvaluateBlockDisabledIsNoop(t *testing.T) {
	p := NewGuardPipeline(NewRuleSet(nil), nil, nil)
	res, err := p.EvaluateBlock(context.Background(), AnchorToolHandler, NewGuardContext())
	testutil.FailErr(t, "eval", err)
	if res.Enforced {
		t.Fatal("disabled anchor must not enforce")
	}
}

func TestEvaluateBlockEmptyWhenFires(t *testing.T) {
	// [OAR-EVAL-3]: a selected rule with neither when nor flow fires.
	ensureCatalog(t)
	l, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	r := &Rule{
		ID: "ALWAYS", Kind: KindPolicy, Anchor: AnchorToolPreInvoke,
		Effect: EffectWarn, Enforcement: "enforce",
	}
	p := NewGuardPipeline(NewRuleSet([]*Rule{r}), l, NewCounterStore())
	p.EnableAnchor(AnchorToolPreInvoke)
	res, err := p.EvaluateBlock(context.Background(), AnchorToolPreInvoke, NewGuardContext())
	testutil.FailErr(t, "eval", err)
	if res.Decision == nil || res.Decision.Effect != EffectWarn || res.Decision.Code != "ALWAYS" {
		t.Fatalf("empty when must fire: %#v", res.Decision)
	}
}
