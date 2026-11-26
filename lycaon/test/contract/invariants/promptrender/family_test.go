package promptrender_test

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

func TestMain(m *testing.M) {
	// Generated worktree cases run the pinned Git.
	gittestsetup.Enable()
	// Production readers consult the process's active catalog; the family
	// judges the stock catalog.
	stock, err := promptrender.LoadStock(context.Background())
	if err != nil {
		println("promptrender: load the stock catalog:", err.Error())
		os.Exit(1)
	}
	extpacks.SetActive(stock.Catalog)
	// Guidance cases render through the process renderer a session build
	// installs; no rule may depend on another installing it first.
	guidancetestsetup.Install()
	code := m.Run()
	if corpus, err := invariants.Shared(); err == nil {
		if err := corpus.Close(); err != nil {
			println("promptrender:", err.Error())
			code = 1
		}
	}
	os.Exit(code)
}

// The prompt-render family runs in its own test binary: its render matrix and
// the Go-source families together exceed one package's memory budget.
func TestPromptRenderInvariants(t *testing.T) {
	t.Parallel()
	corpus, err := invariants.Shared()
	contractcheck.FailErr(t, "invariants.Shared failed", err)
	for _, rule := range promptrender.Family.Invariants {
		t.Run(rule.Name, func(t *testing.T) {
			t.Parallel()
			if res := invariants.Evaluate(corpus, rule); res.Failed() {
				t.Error(res.String())
			}
		})
	}
}
