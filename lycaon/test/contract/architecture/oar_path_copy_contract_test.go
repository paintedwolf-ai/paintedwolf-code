package contract

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// oarPathCopyCodes enumerates the path-related OAR codes whose copy must speak
// the session-folder / @label vocabulary (INV-WR-03): no host absolute paths
// of attached roots, no `label → /abs/path` folder lists.
var oarPathCopyCodes = []string{
	"UNKNOWN_ROOT_LABEL",
	"ROOT_NOT_ATTACHED",
	"CWD_SCRATCH_NOT_VERIFICATION",
	"CWD_OUT_OF_SCOPE",
	"CWD_NOT_DIRECTORY",
	"WORKER_WORKSPACE_REQUIRED",
	"SESSION_SCRATCH_UNAVAILABLE",
	"SESSION_OUTPUT_UNAVAILABLE",
	"COMMAND_OUTPUT_READ_UNAVAILABLE",
	"SURVEY_PATH_ESCAPE",
	"SUMMARIZE_PATHS_MISSING",
	"READ_PATH_NOT_FOUND",
	"LIST_DIR_PATH_NOT_FOUND",
	"GREP_PATH_NOT_FOUND",
	"FIND_PATH_NOT_FOUND",
	"JQ_PATH_NOT_FOUND",
	"JQ_PATH_REQUIRED",
	"EXTRACT_NOT_FOUND",
	"GIT_COMMIT_NO_SESSION_AUTHORSHIP",
	"GIT_PATH_DENIED",
	"GIT_COMMIT_PATH_DENIED",
	"GIT_RESTORE_PATH_DENIED",
	"GIT_BISECT_DIRTY_TREE",
	"COORDINATOR_INVESTIGATE_DENIED_PATH",
	"COORDINATOR_OVERLAY_PATH_READ_FORBIDDEN",
	"COORDINATOR_READ_OUTSIDE_SCOPE",
	"WRITE_SCOPE_DENIED",
	"RENDER_ASSET_DENIED",
	"RENDER_ASSET_NOT_FOUND",
	"CAPTURE_NAVIGATION_DENIED",
	"CAPTURE_PROJECT_DIR_INVALID",
	"CAPTURE_PROJECT_DIR_MISSING",
	"CAPTURE_STATIC_ENTRY_MISSING",
	"SCAN_COMPARE_PROJECT_MISMATCH",
	"SCAN_NOT_FOUND",
	"SCAN_PROJECT_DIR_HOST",
	"SCAN_PACK_NO_HOST_ENGINE",
	"COMMAND_PWD_NOT_CWD",
	"CHMOD_SYMLINK_ESCAPE",
	"SANDBOX_CONTROL_PLANE_DENIED",
}

// AC-WR06-01: every path-related OAR code renders its scenarios with the
// attached_folders the host actually emits for a session whose active root is
// not the primary root, and no rendered copy contains an attached-root host
// path or the retired `label → /abs/path` folder form.
func TestOarPathCopyScenariosRenderWithoutHostPaths(t *testing.T) {
	guidancetestsetup.Install()
	fix := newMultiRootFixture(t)
	primary := fix.roots[0]
	active := fix.roots[1]
	fix.activeID = active.ID
	if !primary.IsPrimary || active.IsPrimary {
		t.Fatalf("fixture must have active != primary: primary=%+v active=%+v", primary, active)
	}
	tctx := fix.tctx("s1")
	tctx.Host.SessionScratchDir = t.TempDir()
	tctx.Host.SessionOutputDir = t.TempDir()
	attached := tools.AttachedFolders(tctx)
	for _, token := range attached {
		if strings.Contains(token, fix.primaryDir) || strings.Contains(token, fix.secondaryDir) {
			t.Fatalf("AttachedFolders leaked a host path: %q", token)
		}
	}
	if !slices.Contains(attached, "@lycaon-den (session folder)") {
		t.Fatalf("active root not marked as session folder: %v", attached)
	}

	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load stock hint config", err)
	formatter := guidance.NewStaticRejectFormatter(cfg)
	for _, code := range oarPathCopyCodes {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Errorf("path-related code %q missing from stock hint config", code)
			continue
		}
		if len(entry.Scenarios) == 0 {
			t.Errorf("path-related code %q declares no scenarios", code)
			continue
		}
		usesFolders := copyUsesAttachedFolders(entry)
		for _, sc := range entry.Scenarios {
			vars := guidance.ScenarioVars(entry, sc)
			vars["attached_folders"] = attached
			body, err := formatter.Format(code, vars)
			if err != nil {
				t.Errorf("%s scenario %q: render failed: %v", code, sc.ID, err)
				continue
			}
			if strings.Contains(body, fix.primaryDir) || strings.Contains(body, fix.secondaryDir) {
				t.Errorf("%s scenario %q: rendered copy leaks an attached-root host path", code, sc.ID)
			}
			if strings.Contains(body, " → ") || strings.Contains(body, " → /") {
				t.Errorf("%s scenario %q: rendered copy uses the retired `label → /abs` folder form", code, sc.ID)
			}
			if usesFolders && !strings.Contains(body, "@lycaon-den (session folder)") {
				t.Errorf("%s scenario %q: copy renders attached_folders without the session-folder marker: %q", code, sc.ID, body)
			}
		}
	}
}

// copyUsesAttachedFolders reports whether any copy field renders the
// attached_folders fact.
func copyUsesAttachedFolders(entry guidance.HintEntry) bool {
	for _, field := range []string{entry.What, entry.Cause, entry.Why, entry.Fix, entry.Instead} {
		if strings.Contains(field, "attached_folders") {
			return true
		}
	}
	return false
}
