// Package vendorpin holds the invariants over vendored third-party trees: each
// names an immutable upstream commit and the bytes it produced, and ships
// as plain tracked files.
package vendorpin

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

const rulesDir = "lycaon/config/runtime/scanners/rules"

// Family is the vendor-pin rule table.
var Family = invariants.Family{Name: "vendor-pin", Invariants: []invariants.Invariant{
	{Name: "scan-rule-vendors-are-pinned", Anchors: []invariants.Anchor{provenance}, Check: scanRuleVendorsArePinned},
	{Name: "scan-rule-patches-are-pinned", Anchors: []invariants.Anchor{provenance}, Check: scanRulePatchesArePinned},
	{Name: "scan-rule-vendors-carry-rule-files", Anchors: []invariants.Anchor{provenance}, Check: scanRuleVendorsCarryRuleFiles},
	{Name: "scan-rule-trees-hold-only-regular-files", Anchors: []invariants.Anchor{invariants.Dir(rulesDir + "/vendor")}, Check: scanRuleTreesHoldOnlyRegularFiles},
	{Name: "scan-rule-files-are-tracked", Anchors: []invariants.Anchor{invariants.Dir(rulesDir + "/vendor")}, Check: scanRuleFilesAreTracked},
	{Name: "zxcvbn-passwords-are-pinned", Anchors: []invariants.Anchor{invariants.Func("internal/secretmint", "LoadProvenance")}, Check: zxcvbnPasswordsArePinned},
}}

var (
	provenance = invariants.Func("internal/scan/rules", "LoadRulesProvenance")
	commitSHA  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	treeSHA    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func vendors(r *invariants.Run) []rules.VendorRulesProvenance {
	prov, err := rules.LoadRulesProvenance()
	r.Must(err)
	r.Expect(len(prov.Vendors), "scan rule vendors")
	return prov.Vendors
}

// pinned reports what a vendoring ledger entry lacks: an immutable commit,
// the digest of the tree it produced, an https upstream, include paths.
func pinned(r *invariants.Run, id, commit, tree, upstream string, include []string, script string) {
	if !commitSHA.MatchString(commit) {
		r.Report("%s: commit %q is not a 40-hex SHA; a ref alone is whatever upstream pushed last", id, commit)
	}
	if !treeSHA.MatchString(tree) {
		r.Report("%s: tree_sha256 %q is not a 64-hex digest; run %s", id, tree, script)
	}
	if !strings.HasPrefix(upstream, "https://") {
		r.Report("%s: upstream %q is not https", id, upstream)
	}
	if len(include) == 0 {
		r.Report("%s: no include paths", id)
	}
}

func scanRuleVendorsArePinned(r *invariants.Run) {
	for _, v := range vendors(r) {
		pinned(r, v.ID, v.Commit, v.TreeSHA256, v.Upstream, v.Include, "scripts/vendor-scan-rules.sh --only "+v.ID)
		if len(v.Paths) == 0 {
			r.Report("%s: no paths entries", v.ID)
		}
	}
}

// A catalog with local fixes records the patch set and what ships, so the
// vendor check tells a hand-edited rule from a patched one offline.
func scanRulePatchesArePinned(r *invariants.Run) {
	for _, v := range vendors(r) {
		patches, err := filepath.Glob(r.Path(rulesDir + "/patches/" + v.ID + "/*.patch"))
		r.Must(err)
		patched := len(patches) > 0
		switch {
		case !patched && (v.PatchSHA256 != "" || v.VendoredSHA256 != ""):
			r.Report("%s: has no patches but pins patch_sha256 or vendored_sha256; run scripts/vendor-scan-rules.sh --only %s", v.ID, v.ID)
		case patched && !treeSHA.MatchString(v.PatchSHA256):
			r.Report("%s: carries patches but patch_sha256 %q is not a 64-hex digest", v.ID, v.PatchSHA256)
		case patched && !treeSHA.MatchString(v.VendoredSHA256):
			r.Report("%s: carries patches but vendored_sha256 %q is not a 64-hex digest", v.ID, v.VendoredSHA256)
		case patched && v.VendoredSHA256 == v.TreeSHA256:
			r.Report("%s: vendored_sha256 equals tree_sha256; its patches change nothing", v.ID)
		}
	}
}

// A vendored catalog holds rule YAML, or the gate runs without it.
func scanRuleVendorsCarryRuleFiles(r *invariants.Run) {
	for _, v := range vendors(r) {
		// paths entries are relative to rules/, so a catalog lives at
		// rules/<id> or rules/vendor/<id>.
		dir := rulesDir + "/" + v.ID
		if _, err := os.Stat(r.Path(dir)); err != nil {
			dir = rulesDir + "/vendor/" + v.ID
		}
		count := 0
		for _, e := range r.Entries(dir) {
			if e.Mode.IsRegular() && (strings.HasSuffix(e.Rel, ".yaml") || strings.HasSuffix(e.Rel, ".yml")) {
				count++
			}
		}
		if count == 0 {
			r.Report("%s: the vendored catalog holds no rule YAML", v.ID)
		}
	}
}

// A nested .git turns a catalog into a submodule pointer no clone
// materializes; a symlink points the bundler outside what the pin covers.
func scanRuleTreesHoldOnlyRegularFiles(r *invariants.Run) {
	for _, e := range r.Entries(rulesDir + "/vendor") {
		switch {
		case strings.HasSuffix(e.Rel, "/.git") || strings.Contains(e.Rel, "/.git/"):
			r.Report("%s: a vendored tree holds .git", e.Rel)
		case e.Mode&fs.ModeSymlink != 0:
			r.Report("%s: a vendored tree holds a symlink", e.Rel)
		case !e.Mode.IsDir() && !e.Mode.IsRegular():
			r.Report("%s: a vendored tree holds a non-regular file", e.Rel)
		}
	}
}

// A vendored file a clone would miss (ignored, or behind a submodule entry)
// fails as a missing rule at scan time rather than an error.
func scanRuleFilesAreTracked(r *invariants.Run) {
	tracked := map[string]bool{}
	for _, g := range r.GitFiles(rulesDir + "/vendor") {
		if g.Mode == "160000" {
			r.Report("%s is a submodule pointer, not vendored files", g.Rel)
			continue
		}
		tracked[g.Rel] = true
	}
	for _, e := range r.Entries(rulesDir + "/vendor") {
		if e.Mode.IsRegular() && !tracked[e.Rel] {
			r.Report("%s is vendored on disk but not tracked; a clone would miss it", e.Rel)
		}
	}
}

func zxcvbnPasswordsArePinned(r *invariants.Run) {
	list, err := secretmint.LoadProvenance()
	r.Must(err)
	r.Expect(len(list), "zxcvbn vendors")
	for _, v := range list {
		pinned(r, v.ID, v.Commit, v.TreeSHA256, v.Upstream, v.Include, "scripts/vendor-zxcvbn-passwords.sh")
	}
}
