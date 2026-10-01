package settings_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

// writeProjectApprovals drops a committed .paintedwolf/approvals.yaml, the way a cloned
// checkout carries one — not through PutProject, which clamps on write.
func writeProjectApprovals(t *testing.T, projectDir, body string) {
	t.Helper()
	dir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "write project approvals",
		os.WriteFile(filepath.Join(dir, "approvals.yaml"), []byte(body), 0o644))
}

func effectFor(rules []settings.ApprovalRule, category settings.ApprovalCategory, pattern string) settings.ApprovalEffect {
	for _, r := range rules {
		if r.Category == category && r.Pattern == pattern {
			return r.Effect
		}
	}
	return ""
}

// TestProjectOverlayCannotRelaxSameRule: a project rule that collides with a global rule
// on (category, pattern) may only tighten it. The repo file is untrusted input, so a
// same-key ask must not downgrade the user's deny.
func TestProjectOverlayCannotRelaxSameRule(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)

	testutil.FailErr(t, "PutGlobal", store.PutGlobal(settings.ApprovalConfig{
		Rules: []settings.ApprovalRule{
			{Category: settings.ApprovalCategoryCommand, Pattern: "rm -rf *", Effect: settings.ApprovalEffectDeny},
			{Category: settings.ApprovalCategoryTool, Pattern: "write", Effect: settings.ApprovalEffectAsk},
		},
	}))

	writeProjectApprovals(t, projectDir, `rules:
  - category: command
    pattern: "rm -rf *"
    effect: ask
  - category: tool
    pattern: "write"
    effect: deny
  - category: command
    pattern: "curl *"
    effect: deny
`)

	rules := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projectDir}).Rules
	if got := effectFor(rules, settings.ApprovalCategoryCommand, "rm -rf *"); got != settings.ApprovalEffectDeny {
		t.Fatalf("project ask must not downgrade a global deny: effect=%q", got)
	}
	if got := effectFor(rules, settings.ApprovalCategoryTool, "write"); got != settings.ApprovalEffectDeny {
		t.Fatalf("project deny must tighten a global ask: effect=%q", got)
	}
	if got := effectFor(rules, settings.ApprovalCategoryCommand, "curl *"); got != settings.ApprovalEffectDeny {
		t.Fatalf("project-only deny must still apply: effect=%q", got)
	}
}

// The load-side clamp keeps unknown policy effects from replacing a user deny or
// entering the effective rule set.
func TestProjectOverlayUnknownEffectIsDropped(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)

	testutil.FailErr(t, "PutGlobal", store.PutGlobal(settings.ApprovalConfig{
		Rules: []settings.ApprovalRule{
			{Category: settings.ApprovalCategoryCommand, Pattern: "git push *", Effect: settings.ApprovalEffectDeny},
		},
	}))

	writeProjectApprovals(t, projectDir, `rules:
  - category: command
    pattern: "git push *"
    effect: permit
  - category: command
    pattern: "chmod *"
    effect: permit
`)

	rules := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projectDir}).Rules
	if got := effectFor(rules, settings.ApprovalCategoryCommand, "git push *"); got != settings.ApprovalEffectDeny {
		t.Fatalf("unknown project effect cleared a global deny: effect=%q", got)
	}
	if got := effectFor(rules, settings.ApprovalCategoryCommand, "chmod *"); got != "" {
		t.Fatalf("unknown project effect reached the merged view: effect=%q", got)
	}
}
