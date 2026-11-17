package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var readOnlyNativeSurvey = []string{
	"source_history",
	"command",
}

var readOnlyNativeMutations = []string{
	"write", "edit", "replace_lines", "restore_version", "code_rewrite", "jq_edit", "chmod", "delete",
	"git_commit", "git_restore", "extract_archive",
}

func loadToolProfileByID(t *testing.T, id string) sandbox.ToolProfile {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	for _, p := range profiles {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("profile %q missing", id)
	return sandbox.ToolProfile{}
}

func profileGrants(prof sandbox.ToolProfile, tool string) bool {
	if prof.Tools[tool] {
		return true
	}
	for _, deny := range prof.DenyTools {
		if deny == tool {
			return false
		}
	}
	return false
}

func TestProfileToolTier_implementHasVerify(t *testing.T) {
	t.Parallel()
	prof := loadToolProfileByID(t, "implement")
	if !prof.Tools["verify"] {
		t.Fatal("implement must include verify")
	}
	if !prof.Tools["command"] {
		t.Fatal("implement must include command")
	}
}

func TestProfileToolTier_readOnlyProfilesDenyMutations(t *testing.T) {
	t.Parallel()
	readOnly, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	for _, id := range []string{"explore_readonly", "worker_readonly", "plan_review_readonly", "web_research"} {
		prof := loadToolProfileByID(t, id)
		for _, tool := range readOnlyNativeMutations {
			if profileGrants(prof, tool) {
				t.Fatalf("%s must not grant mutation tool %q", id, tool)
			}
		}
		if id == "web_research" {
			if profileGrants(prof, "command") {
				t.Fatalf("%s must not grant command", id)
			}
			continue
		}
		if !prof.Tools["command"] || !prof.ToolSticky("command") || prof.CommandEffects != readOnly {
			t.Fatalf("%s must grant sticky read_only command; got allowed=%v sticky=%v effects=%v", id, prof.Tools["command"], prof.ToolSticky("command"), prof.CommandEffects.Classes())
		}
		for _, tool := range readOnlyNativeSurvey {
			if !prof.Tools[tool] {
				t.Fatalf("%s missing read-only survey tool %q", id, tool)
			}
		}
	}
}
