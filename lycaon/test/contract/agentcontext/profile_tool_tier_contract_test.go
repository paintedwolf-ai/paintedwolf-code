package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var implementNativeToolkit = []string{
	"read", "write", "edit", "replace_lines", "restore_version", "code_rewrite",
	"grep", "find", "stat", "wc", "list_dir", "chmod", "delete",
	"copy", "move", "mkdir", "chown", "diff", "jq", "jq_edit", "extract_archive",
	"source_history", "command",
}

var coordinatorNativeCeiling = []string{
	"read", "write", "edit", "replace_lines", "restore_version", "code_rewrite",
	"grep", "find", "stat", "wc", "list_dir", "chmod", "delete",
	"diff", "jq",
	"git_status", "git_diff", "git_log", "git_commit", "git_show",
	"git_blame", "source_history", "git_restore", "git_ref", "git_branches",
}

var readOnlyNativeSurvey = []string{
	"read", "grep", "find", "jq", "stat", "wc", "list_dir",
	"git_status", "git_diff", "git_log", "git_show", "git_blame", "source_history", "git_ref", "git_branches",
}

var readOnlyNativeMutations = []string{
	"write", "edit", "replace_lines", "restore_version", "code_rewrite", "jq_edit", "chmod", "delete",
	"git_commit", "git_restore", "command", "extract_archive",
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

func TestProfileToolTier_implementHasFullNativeToolkit(t *testing.T) {
	t.Parallel()
	prof := loadToolProfileByID(t, "implement")
	for _, tool := range implementNativeToolkit {
		if !prof.Tools[tool] {
			t.Fatalf("implement profile missing full-toolkit native %q", tool)
		}
	}
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

func TestProfileToolTier_implementHasScanDrillDown(t *testing.T) {
	t.Parallel()
	prof := loadToolProfileByID(t, "implement")
	for _, tool := range []string{"scan_list", "scan_summary", "scan_query", "scan_pack", "scan_compare"} {
		if !prof.Tools[tool] {
			t.Fatalf("implement profile missing scan tool %q", tool)
		}
	}
}

func TestProfileToolTier_readOnlyProfilesDenyMutations(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"explore_readonly", "worker_readonly", "plan_review_readonly", "web_research"} {
		prof := loadToolProfileByID(t, id)
		for _, tool := range readOnlyNativeMutations {
			if profileGrants(prof, tool) {
				t.Fatalf("%s must not grant mutation tool %q", id, tool)
			}
		}
		for _, tool := range readOnlyNativeSurvey {
			if id == "web_research" {
				continue
			}
			if !prof.Tools[tool] {
				t.Fatalf("%s missing read-only survey tool %q", id, tool)
			}
		}
	}
}

func TestProfileToolTier_coordinatorCeilingIncludesInvestigateNativeTools(t *testing.T) {
	t.Parallel()
	prof := loadToolProfileByID(t, "coordinator")
	for _, tool := range coordinatorNativeCeiling {
		if !prof.Tools[tool] {
			t.Fatalf("coordinator profile missing native ceiling tool %q", tool)
		}
	}
}

func TestProfileToolTier_commandEquivalenceProfilesMatchYAML(t *testing.T) {
	t.Parallel()
	eq := loadToolCommandEquivalenceYAML(t)
	for _, entry := range eq.Entries {
		for _, profileID := range entry.Profiles {
			prof := loadToolProfileByID(t, profileID)
			if !prof.Tools[entry.Native] {
				t.Fatalf("tool-command-equivalence lists %q on %q but profile omits it", entry.Native, profileID)
			}
		}
	}
}

// repoFacingProfiles lists tool profiles that survey the product repo.
// web_research is excluded — external URLs only, no repo material.
var repoFacingProfiles = []string{
	"implement", "explore_readonly", "coordinator", "worker_readonly",
	"plan_review_readonly", "plan_write_only",
}

func TestProfileToolTier_repoFacingProfilesHaveSummarize(t *testing.T) {
	t.Parallel()
	for _, id := range repoFacingProfiles {
		prof := loadToolProfileByID(t, id)
		if !prof.Tools["summarize"] {
			t.Fatalf("%s must include summarize", id)
		}
	}
}
