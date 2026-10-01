package cost

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/testutil"
)

type coverageKindStub struct{ kind string }

func (s coverageKindStub) ProviderKind(string) (string, bool)     { return s.kind, true }
func (s coverageKindStub) PricedAsModelID(_, model string) string { return model }
func (s coverageKindStub) LocalFree(string) bool                  { return false }

// TestModelfeedEligibleModelsPriced covers every eligible fixture model.
func TestModelfeedEligibleModelsPriced(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fixture := filepath.Join(filepath.Dir(thisFile), "..", "modelfeed", "testdata", "models_dev_fixture.json")
	raw, err := os.ReadFile(fixture)
	testutil.FailErr(t, "read models.dev fixture", err)
	doc, err := modelfeed.ParseDocument(raw)
	testutil.FailErr(t, "parse models.dev fixture", err)

	table := pricing.RateTableFromDocument(doc)
	source := &PricedSource{ID: "models-dev", Table: table}

	for _, kind := range modelfeed.MappedKinds() {
		t.Run(kind, func(t *testing.T) {
			feedKey, ok := modelfeed.FeedKeyForKind(kind)
			if !ok {
				t.Fatalf("kind %q has no feed key mapping", kind)
			}
			eligible := doc.EligibleModels(feedKey)
			if len(eligible) == 0 {
				t.Fatalf("fixture feed %q has no ConversationEligible models for kind %q", feedKey, kind)
			}

			pricer := NewChainPricer(nil, coverageKindStub{kind: kind}, source)
			var missing []string
			priced := 0
			for _, m := range eligible {
				modelID := strings.TrimSpace(m.ID)
				if modelID == "" {
					t.Fatalf("eligible model with empty id under feed %q", feedKey)
				}
				est, err := pricer.EstimateCost(kind, modelID, TokenUsage{
					PromptTokens:     100,
					CompletionTokens: 50,
				})
				testutil.FailErr(t, "EstimateCost", err)
				if est.Unpriced {
					missing = append(missing, modelID)
					continue
				}
				priced++
			}
			if len(missing) > 0 {
				limit := len(missing)
				if limit > 10 {
					limit = 10
				}
				t.Fatalf("kind %q: %d eligible models missing rates (showing up to 10): %v",
					kind, len(missing), missing[:limit])
			}
			if priced == 0 {
				t.Fatalf("kind %q: expected at least one priced eligible model", kind)
			}
		})
	}
}
