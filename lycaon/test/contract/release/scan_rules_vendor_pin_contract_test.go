package contract

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/scan/rules"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var (
	vendorCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
	vendorTreeSHA   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// TestVendorRuleCatalogsArePinned holds the vendoring contract: every catalog
// names an immutable commit and the bytes that commit produced. A ref alone is a
// branch tip, which is whatever upstream pushed last.
func TestVendorRuleCatalogsArePinned(t *testing.T) {
	t.Parallel()
	prov, err := rules.LoadRulesProvenance()
	contractcheck.FailErr(t, "rules.LoadRulesProvenance failed", err)

	for _, vendor := range prov.Vendors {
		if !vendorCommitSHA.MatchString(vendor.Commit) {
			t.Errorf("%s: commit %q is not a 40-hex SHA — vendoring must pin an immutable object", vendor.ID, vendor.Commit)
		}
		if !vendorTreeSHA.MatchString(vendor.TreeSHA256) {
			t.Errorf("%s: tree_sha256 %q is not a 64-hex digest — run scripts/vendor-scan-rules.sh --only %s", vendor.ID, vendor.TreeSHA256, vendor.ID)
		}
		if !strings.HasPrefix(vendor.Upstream, "https://") {
			t.Errorf("%s: upstream %q is not https", vendor.ID, vendor.Upstream)
		}
		if len(vendor.Include) == 0 {
			t.Errorf("%s: no include: paths — the vendor script would not know what to copy", vendor.ID)
		}
		if len(vendor.Paths) == 0 {
			t.Errorf("%s: no paths: entries", vendor.ID)
		}
	}
}

// TestVendorRulePatchesArePinned holds the patch-layer contract: a catalog that
// carries local fixes records both the patch set and what ships, so --check can
// tell a hand-edited rule from a patched one offline.
func TestVendorRulePatchesArePinned(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	prov, err := rules.LoadRulesProvenance()
	contractcheck.FailErr(t, "rules.LoadRulesProvenance failed", err)

	for _, vendor := range prov.Vendors {
		dir := filepath.Join(
			root, "lycaon", "config", "runtime", "scanners", "rules", "patches", vendor.ID,
		)
		patched := false
		if entries, statErr := os.ReadDir(dir); statErr == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".patch") {
					patched = true
					break
				}
			}
		}

		if !patched {
			if vendor.PatchSHA256 != "" || vendor.VendoredSHA256 != "" {
				t.Errorf(
					"%s: has no patches but pins patch_sha256/vendored_sha256 — run scripts/vendor-scan-rules.sh --only %s",
					vendor.ID, vendor.ID,
				)
			}
			continue
		}
		if !vendorTreeSHA.MatchString(vendor.PatchSHA256) {
			t.Errorf(
				"%s: carries patches but patch_sha256 %q is not a 64-hex digest",
				vendor.ID, vendor.PatchSHA256,
			)
		}
		// The patched bytes must differ from the pin, or the patch does nothing
		// and the ledger would claim a fix that never lands.
		if !vendorTreeSHA.MatchString(vendor.VendoredSHA256) {
			t.Errorf(
				"%s: carries patches but vendored_sha256 %q is not a 64-hex digest",
				vendor.ID, vendor.VendoredSHA256,
			)
		}
		if vendor.VendoredSHA256 == vendor.TreeSHA256 {
			t.Errorf("%s: vendored_sha256 equals tree_sha256 — its patches change nothing", vendor.ID)
		}
	}
}

// TestVendorRuleCatalogsCarryRuleFiles rejects empty vendor catalogs.
func TestVendorRuleCatalogsCarryRuleFiles(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	prov, err := rules.LoadRulesProvenance()
	contractcheck.FailErr(t, "rules.LoadRulesProvenance failed", err)

	for _, vendor := range prov.Vendors {
		dir := filepath.Join(root, "lycaon", "config", "runtime", "scanners", "rules", vendor.ID)
		if _, err := os.Stat(dir); err != nil {
			// paths: are relative to rules/, so vendor/<id>/ is the on-disk home.
			dir = filepath.Join(root, "lycaon", "config", "runtime", "scanners", "rules", "vendor", vendor.ID)
		}
		count := 0
		walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".yaml", ".yml":
				count++
			}
			return nil
		})
		contractcheck.FailErr(t, "walk vendored catalog", walkErr)
		if count == 0 {
			t.Errorf("%s: vendored catalog holds no rule YAML — the gate would run without it", vendor.ID)
		}
	}
}

// TestVendorRuleTreesHoldOnlyRegularFiles rejects the two shapes that make a
// vendored tree lie about what it ships: a nested .git, which turns the whole
// catalog into a submodule pointer that no clone materializes, and a symlink,
// which points the bundler somewhere the pin does not cover.
func TestVendorRuleTreesHoldOnlyRegularFiles(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	vendorDir := filepath.Join(root, "lycaon", "config", "runtime", "scanners", "rules", "vendor")

	err := filepath.WalkDir(vendorDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(vendorDir, path)
		if relErr != nil {
			rel = path
		}
		if d.Name() == ".git" {
			t.Errorf("%s: vendored tree contains .git — git records the parent as a submodule pointer and the rules stop shipping", rel)
			return fs.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			t.Errorf("%s: vendored tree contains a symlink", rel)
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			t.Errorf("%s: vendored tree contains a non-regular file", rel)
		}
		return nil
	})
	contractcheck.FailErr(t, "walk vendor tree", err)
}

// TestVendorRuleFilesAreTracked catches a vendored file that exists here but not
// in a clone — swallowed by a .gitignore pattern, or left behind by a submodule
// entry. Both fail as a missing rule at scan time rather than as an error.
func TestVendorRuleFilesAreTracked(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	rel := "lycaon/config/runtime/scanners/rules/vendor"

	cmd := exec.Command("git", "ls-files", "-s", "--", rel)
	cmd.Dir = root
	cmd.Env = lyexec.LocalGitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git ls-files unavailable: %v", err)
	}

	tracked := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[0] == "160000" {
			t.Errorf("%s is a submodule pointer, not vendored files — no clone materializes it", fields[3])
			continue
		}
		tracked[fields[3]] = struct{}{}
	}
	if len(tracked) == 0 {
		t.Fatal("no tracked files under the vendored rule tree")
	}

	vendorDir := filepath.Join(root, filepath.FromSlash(rel))
	err = filepath.WalkDir(vendorDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		p, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if _, ok := tracked[filepath.ToSlash(p)]; !ok {
			t.Errorf("%s is vendored on disk but not tracked — it would be missing from a clone", filepath.ToSlash(p))
		}
		return nil
	})
	contractcheck.FailErr(t, "walk vendor tree", err)
}
