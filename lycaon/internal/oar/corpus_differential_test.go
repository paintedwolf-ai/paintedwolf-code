package oar

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"path/filepath"
	"testing"
)

// [OAR-CONF-38] Every fixture uses production document loading and evaluation.
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
