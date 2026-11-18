package contract

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/datamark"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Shorter windows collide with common identifiers and phrases.
const catalogCopyWindow = 40

// Shared transport markers and retrieval framing require matching catalog text.
var catalogCopyDuplicationAllowed = map[string]string{
	"lycaon/internal/guidance/host_markers.go": "the host marker vocabulary — AGENTS.md § No heuristics " +
		"names this file as the single spelling for markers the host writes and reads back, so the " +
		"catalog and the constant are one vocabulary rather than two copies",
	"lycaon/internal/datamark/datamark.go": "the untrusted-retrieval teaching is rendered into every " +
		"framed body as well as the prompt partial, so it has to exist at the injection point; " +
		"TestUntrustedTeachingMatchesPartial pins the two equal",
}

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

// Format verbs and template tags can interrupt otherwise identical copy.
var copyPlaceholderRE = regexp.MustCompile(`%[+\-# 0-9.]*[a-zA-Z]|\{\{.*?\}\}|\{%.*?%\}`)
