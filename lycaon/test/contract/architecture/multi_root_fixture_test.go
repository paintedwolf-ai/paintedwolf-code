package contract

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// These tools do not accept root-labelled paths.
var multiRootProbeExempt = map[string]string{
	"git_compare":    "revision identities on the active root; no path argument",
	"git_checkout":   "branch identity on the active root; no path argument",
	"git_stash_list": "reflog pagination on the active root; no path argument",
	"git_status":     "paths are repo-relative prefixes on the active root, not root-labelled paths; active-root scoped git surface (git manager contract tests)",
	"git_diff":       "paths are repo-relative pathspecs on the active root, not root-labelled paths; active-root scoped git surface (git manager contract tests)",
	"git_log":        "no path args; active-root scoped git surface (git manager contract tests)",
	"git_commit":     "no path args; active-root scoped git surface (git manager contract tests)",
	"git_show":       "no path args; active-root scoped git surface (git manager contract tests)",
	"git_ref":        "no path args; active-root scoped git surface (git manager contract tests)",
	"git_branches":   "no path args; active-root scoped git surface (git manager contract tests)",
	"pack_board":     "no path args; board snapshot multi-root covered in internal/board/multi_root_snapshot_test.go",
	// The probe plants a .txt path, so this tool answers about the format before
	// it ever looks at the root label — a correct answer to the question asked,
	// and not the one the label probe is testing.
	"extract_archive": "validates archive format before path; label handling covered by extract_archive's own tests",
}

// multiRootProbeExemption returns a reasoned exemption for the probe.
func multiRootProbeExemption(name string) (string, bool) {
	reason, ok := multiRootProbeExempt[strings.ToLower(strings.TrimSpace(name))]
	return reason, ok
}

// multiRootProbeExemptNames returns the exempt tools, sorted.
func multiRootProbeExemptNames() []string {
	out := make([]string, 0, len(multiRootProbeExempt))
	for name := range multiRootProbeExempt {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// multiRootCapabilityOf is the compiled catalog answer.
func multiRootCapabilityOf(name string) toolcontract.MultiRootCapability {
	capability, _ := toolcontract.MultiRootOf(name)
	return capability
}

// multiRootClassified reports whether anyone declared a capability for name.
func multiRootClassified(name string) bool {
	_, ok := toolcontract.MultiRootOf(name)
	return ok
}

var multiRootPathArgProps = map[string]struct{}{
	"path": {}, "paths": {}, "src": {}, "dest": {}, "file": {},
	"source": {}, "destination": {}, "path_a": {}, "path_b": {},
	"from": {}, "to": {},
}

var multiRootModeArgProps = map[string]string{
	"old_string": "probe-old",
	"new_string": "probe-new",
}

type multiRootFixture struct {
	primaryDir   string
	secondaryDir string
	roots        []projectroot.RootRef
	activeID     string
}

func newMultiRootFixture(t *testing.T) multiRootFixture {
	t.Helper()
	base := t.TempDir()
	primary := filepath.Join(base, "lycaon")
	secondary := filepath.Join(base, "lycaon-den")
	for _, dir := range []string{primary, secondary} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			contractcheck.FailErr(t, "mkdir", err)
		}
	}
	if err := os.WriteFile(filepath.Join(primary, "primary-sentinel.txt"), []byte("primary"), 0o644); err != nil {
		contractcheck.FailErr(t, "write primary sentinel", err)
	}
	if err := os.WriteFile(filepath.Join(secondary, "secondary-sentinel.txt"), []byte("secondary"), 0o644); err != nil {
		contractcheck.FailErr(t, "write secondary sentinel", err)
	}
	roots := []projectroot.RootRef{
		{ID: "p", Label: "lycaon", Path: primary, IsPrimary: true},
		{ID: "d", Label: "lycaon-den", Path: secondary, IsPrimary: false},
	}
	return multiRootFixture{primaryDir: primary, secondaryDir: secondary, roots: roots, activeID: "p"}
}

func (f multiRootFixture) tctx(sessionID string) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "proj-1",
			SessionID: sessionID,
			Agent:     "implement"},
		Source: tools.InvocationSource{Roots: f.roots,
			ActiveRootID:       f.activeID,
			RepoFileCount:      100,
			RepoFileCountKnown: true},
	}
}

func toolRejectCode(err error) string {
	if err == nil {
		return ""
	}
	var reject *toolrejection.ToolReject
	if errors.As(err, &reject) && reject != nil {
		return reject.Code
	}
	if refusal, ok := guidance.RefusalFromError(err); ok {
		return refusal.Code()
	}
	return ""
}
