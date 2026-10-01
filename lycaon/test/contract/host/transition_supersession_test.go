package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every execution-mode transition partial closes on the same supersession
// sentence, which lives in one partial so they cannot drift apart.
func TestTransitionPartialsIncludeSupersession(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	partialsDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials")
	const include = `{% include "partials/execution-mode-supersession.md" %}`

	shared := filepath.Join(partialsDir, "execution-mode-supersession.md")
	body, err := os.ReadFile(shared)
	if err != nil {
		t.Fatalf("read execution-mode-supersession.md: %v", err)
	}
	if strings.TrimSpace(string(body)) == "" {
		t.Fatal("execution-mode-supersession.md is empty — the transition partials would close on nothing")
	}

	for _, name := range []string{
		"coordinator-mode-entered-investigate.md",
		"coordinator-mode-entered-orchestrate.md",
		"coordinator-mode-entered-wrapup.md",
		"coordinator-mode-left-investigate.md",
		"coordinator-mode-left-orchestrate.md",
		"coordinator-mode-left-wrapup.md",
	} {
		raw, err := os.ReadFile(filepath.Join(partialsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := strings.TrimSpace(string(raw))
		if !strings.HasSuffix(text, include) {
			t.Errorf("%s must end with %s", name, include)
		}
		if strings.Contains(text, "Prior turns may reference") {
			t.Errorf("%s restates the supersession line inline — render the partial instead", name)
		}
	}
}
