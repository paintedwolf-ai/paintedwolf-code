package contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestDistinctPersonaRenderHash(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	prompts.ResetPersonaContractCache()
	engine := contractPersonaEngine(t)
	impl, err := prompts.RenderPersona(context.Background(), engine, "implementer", nil)
	contractcheck.FailErr(t, "prompts.RenderPersona failed", err)
	reviewer, err := prompts.RenderPersona(context.Background(), engine, "plan-reviewer", nil)
	contractcheck.FailErr(t, "prompts.RenderPersona failed", err)
	if sha256Hex(impl) == sha256Hex(reviewer) {
		t.Fatal("implementer and plan-reviewer rendered hashes must differ")
	}
	implFocus := extractSection(impl, "## Focus")
	reviewFocus := extractSection(reviewer, "## Focus")
	if implFocus == reviewFocus || implFocus == "" || reviewFocus == "" {
		t.Fatalf("Focus sections must differ: implementer=%q reviewer=%q", implFocus, reviewFocus)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func extractSection(body, heading string) string {
	idx := strings.Index(body, heading)
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(heading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

func TestRenderedOutputHasRequiredHeadings(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	prompts.ResetPersonaContractCache()
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	engine := contractPersonaEngine(t)
	for _, id := range cfg.WorkerAgentIDs() {
		got, err := prompts.RenderPersona(context.Background(), engine, id, nil)
		if err != nil {
			t.Fatalf("agent %q: %v", id, err)
		}
		for _, heading := range cfg.RequiredHeadingsRendered {
			// Required headings appear exactly once.
			switch n := countHeadingLines(got, heading); {
			case n == 0:
				t.Fatalf("agent %q missing %s", id, heading)
			case n > 1:
				t.Fatalf("agent %q renders %s %d times — drop the duplicate; the archetype/partial already provides it", id, heading, n)
			}
		}
	}
}

// countHeadingLines counts exact heading lines.
func countHeadingLines(body, heading string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimRight(line, " \t") == heading {
			n++
		}
	}
	return n
}
