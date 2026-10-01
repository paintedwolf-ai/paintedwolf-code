package contract

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestImplementerPromptTeachesFirstClassHTTPRequest(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	prompts.ResetPersonaContractCache()
	// HTTP tools are loadable on the implementer profile: untaught until the
	// leg loads them, taught as first-class actions once it does.
	bare, err := prompts.RenderPersona(context.Background(), contractPersonaEngine(t), "implementer", nil)
	contractcheck.FailErr(t, "render implementer persona", err)
	if strings.Contains(bare, "### HTTP actions") {
		t.Fatal("implementer prompt teaches HTTP actions before the tools load")
	}
	rendered, err := prompts.RenderPersona(context.Background(), contractPersonaEngine(t), "implementer", map[string]any{
		"loaded_tools": map[string]bool{"http_request": true, "fetch_url": true},
	})
	contractcheck.FailErr(t, "render implementer persona with HTTP tools", err)
	for _, want := range []string{
		"### HTTP actions",
		"Use `http_request` for APIs",
		"Use `fetch_url` for readable web research",
		"Use `wait` with `http_ready`/`port_ready`",
		"Call service APIs with `http_request`",
		"use `wait` readiness conditions instead of request loops",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("implementer prompt missing %q", want)
		}
	}
	if containsWord(strings.ToLower(rendered), "curl") {
		t.Fatal("implementer prompt advertises a shell HTTP client")
	}
}

// Every pack's agent-facing copy — prompts, partials, kicks, OAR policy,
// playbooks, skills, schemas, and approval explanations — teaches the native
// tool and never a shell client. Host data (detection rules, secret patterns,
// approval-rule fixtures) may name one; it is matched, not taught.
func TestHTTPRequestAgentTeachingDoesNotAdvertiseShellClient(t *testing.T) {
	t.Parallel()
	packs := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf")
	entries, err := os.ReadDir(packs)
	contractcheck.FailErr(t, "list packs", err)
	for _, pack := range entries {
		if !pack.IsDir() {
			continue
		}
		for _, rel := range []string{
			"agents/prompts",
			"shared/partials",
			"guidance",
			"policy",
			"playbooks",
			"skills",
			"tools/schemas",
			"approvals",
		} {
			dir := filepath.Join(packs, pack.Name(), rel)
			if _, err := os.Stat(dir); err != nil {
				continue
			}
			err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				lower := strings.ToLower(string(raw))
				for _, client := range []string{"curl", "wget", "httpie"} {
					if containsWord(lower, client) {
						t.Errorf("agent-facing HTTP teaching names a shell client %q: %s", client, path)
					}
				}
				return nil
			})
			contractcheck.FailErr(t, "walk agent-facing HTTP teaching", err)
		}
	}
}
