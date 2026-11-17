package laws_test

import (
	"os"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/laws"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if corpus, err := laws.Shared(); err == nil {
			if stats := corpus.Stats(); stats.GoParses > 1 || stats.TypeChecks > 1 {
				println("laws: the Go corpus loaded more than once:", stats.GoParses, "parses,", stats.TypeChecks, "type-checks")
				code = 1
			}
		}
	}
	os.Exit(code)
}

// TestLaws evaluates every rule of every family against the repository.
func TestLaws(t *testing.T) {
	t.Parallel()
	corpus, err := laws.Shared()
	contractcheck.FailErr(t, "laws.Shared failed", err)
	for _, family := range families {
		t.Run(family.Name, func(t *testing.T) {
			t.Parallel()
			if len(family.Rules) == 0 {
				t.Fatal("family has no rules")
			}
			seen := map[string]bool{}
			for _, rule := range family.Rules {
				if seen[rule.Name] {
					t.Errorf("rule %s is declared twice", rule.Name)
					continue
				}
				seen[rule.Name] = true
				t.Run(rule.Name, func(t *testing.T) {
					t.Parallel()
					if res := laws.Evaluate(corpus, rule); res.Failed() {
						t.Error(res.String())
					}
				})
			}
		})
	}
}
