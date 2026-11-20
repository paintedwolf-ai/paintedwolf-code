package contract

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/datamark"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Retrieval bodies and prompts carry the same trust notice: the rendered
// untrusted-output partial is one line of every framed retrieval body.
func TestUntrustedTeachingMatchesPartial(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	rendered, err := engine.Render(t.Context(), "partials/untrusted-output.md", map[string]any{})
	contractcheck.FailErr(t, "render untrusted-output partial", err)
	partial := strings.TrimSpace(rendered)
	if partial == "" {
		t.Fatal("untrusted-output partial renders empty")
	}
	framed := datamark.Frame("contract", "retrieved body", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if !slices.Contains(strings.Split(framed, "\n"), partial) {
		t.Fatalf("framed retrieval body does not carry the rendered partial as a line:\npartial: %q\nframed: %q", partial, framed)
	}
}
