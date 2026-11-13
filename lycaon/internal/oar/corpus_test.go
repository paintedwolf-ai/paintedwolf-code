package oar

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"path/filepath"
	"testing"
)

// Every fixture uses production document loading and evaluation.
func TestProductionEngineAgainstPublishedCorpus(t *testing.T) {
	ensureCatalog(t)
	dir := filepath.Join(testutil.CheckoutRoot(t), "schemas", "oar", "conformance")
	outcomes, err := RunCorpusDir(dir)
	testutil.FailErr(t, "run production corpus", err)
	if len(outcomes) == 0 {
		t.Fatal("corpus produced no outcomes")
	}
	for _, outcome := range outcomes {
		t.Run(outcome.ID, func(t *testing.T) {
			if outcome.Status != "pass" {
				t.Fatalf("%s: %s; actual: %#v", outcome.Status, outcome.Reason, outcome.Actual)
			}
			if outcome.Actual == nil {
				t.Fatal("production result is missing")
			}
		})
	}
}

func TestFixtureShapeIsClosed(t *testing.T) {
	ensureCatalog(t)
	for name, raw := range map[string]string{
		"top-level member": `{"id":"x","rule":{},"input":{"anchor":"tool.pre_invoke","facts":{}},"expected":{},"name":"x"}`,
		"input member":     `{"id":"x","rule":{},"input":{"anchor":"tool.pre_invoke","facts":{},"capability":{}},"expected":{}}`,
		"no rule or hints": `{"id":"x","input":{"anchor":"tool.pre_invoke","facts":{}},"expected":{}}`,
		"no expected":      `{"id":"x","rule":{},"input":{"anchor":"tool.pre_invoke","facts":{}}}`,
	} {
		if _, err := decodeFixture([]byte(raw)); err == nil {
			t.Errorf("%s: fixture accepted", name)
		}
	}
	fx, err := decodeFixture([]byte(`{"id":"hints-only","hints":[{"spec_version":"1.0","id":"H","kind":"hint","what":"static"}],"input":{"anchor":"tool.pre_invoke","facts":{"tool":"read"}},"expected":{"decision":"none"}}`))
	testutil.FailErr(t, "decode hints-only fixture", err)
	runner, err := NewConformanceRunner("")
	testutil.FailErr(t, "runner", err)
	testutil.FailErr(t, "run hints-only fixture", runner.RunFixture(fx))
}
