package invariants_test

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

func TestMain(m *testing.M) {
	// Production readers consult the process's active catalog. The invariants judge
	// the stock catalog, so it is active before any rule runs.
	stock, err := promptrender.LoadStock(context.Background())
	if err != nil {
		println("invariants: load the stock catalog:", err.Error())
		os.Exit(1)
	}
	extpacks.SetActive(stock.Catalog)
	code := m.Run()
	if corpus, err := invariants.Shared(); err == nil {
		if stats := corpus.Stats(); stats.GoParses > 1 || stats.TypeChecks > 1 {
			println("invariants: the Go corpus loaded more than once:", stats.GoParses, "parses,", stats.TypeChecks, "type-checks")
			code = 1
		}
		if err := corpus.Close(); err != nil {
			println("invariants:", err.Error())
			code = 1
		}
	}
	os.Exit(code)
}

// TestInvariants evaluates every rule of every family against the repository.
func TestInvariants(t *testing.T) {
	t.Parallel()
	corpus, err := invariants.Shared()
	contractcheck.FailErr(t, "invariants.Shared failed", err)
	for _, family := range families {
		t.Run(family.Name, func(t *testing.T) {
			t.Parallel()
			if len(family.Invariants) == 0 {
				t.Fatal("family has no rules")
			}
			seen := map[string]bool{}
			for _, rule := range family.Invariants {
				if seen[rule.Name] {
					t.Errorf("rule %s is declared twice", rule.Name)
					continue
				}
				seen[rule.Name] = true
				t.Run(rule.Name, func(t *testing.T) {
					t.Parallel()
					if res := invariants.Evaluate(corpus, rule); res.Failed() {
						t.Error(res.String())
					}
				})
			}
		})
	}
}
