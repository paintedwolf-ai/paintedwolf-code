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
