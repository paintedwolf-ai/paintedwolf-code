package confine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
)

// Block-scoped assertions isolate rule precedence.

// Rule order distinguishes the read allow-back block.
const (
	blockAllowWrite    = "(allow file-write*"
	blockDenyWrite     = "(deny file-write*"
	blockDenyRead      = "(deny file-read*"
	blockAllowReadBack = "(allow file-read*"
)

// profileBlock returns the first matching rule block at or after from.
func profileBlock(t *testing.T, profile, opener string, from int) (body string, at int) {
	t.Helper()
	idx := strings.Index(profile[from:], opener)
	if idx < 0 {
		t.Fatalf("profile has no %s block:\n%s", opener, profile)
	}
	at = from + idx
	rest := profile[at:]
	if end := strings.Index(rest, ")\n("); end >= 0 {
		return rest[:end], at
	}
	return rest, at
}

func profileBlockContaining(t *testing.T, profile, opener, needle string) (body string, at int) {
	t.Helper()
	for from := 0; from < len(profile); {
		idx := strings.Index(profile[from:], opener)
		if idx < 0 {
			break
		}
		body, at = profileBlock(t, profile, opener, from)
		if strings.Contains(body, needle) {
			return body, at
		}
		from = at + len(body)
	}
	t.Fatalf("profile has no %s block containing %q:\n%s", opener, needle, profile)
	return "", -1
}

// readAllowBackBlock returns the re-allow block after the secret read deny.
func readAllowBackBlock(t *testing.T, profile string) string {
	t.Helper()
	_, denyAt := profileBlock(t, profile, blockDenyRead, 0)
	body, _ := profileBlock(t, profile, blockAllowReadBack, denyAt)
	return body
}

// assertBlockCoversPath requires a resolved subpath rule.
func assertBlockCoversPath(t *testing.T, block, path, what string) {
	t.Helper()
	if !strings.Contains(block, `(subpath "`+resolvedForProfile(path)+`")`) {
		t.Fatalf("%s must cover %q, block was:\n%s", what, path, block)
	}
}

// assertBlockOmitsPath requires the block to omit path.
func assertBlockOmitsPath(t *testing.T, block, path, what string) {
	t.Helper()
	if strings.Contains(block, `(subpath "`+resolvedForProfile(path)+`")`) {
		t.Fatalf("%s must not cover %q, block was:\n%s", what, path, block)
	}
}

// assertBlockMentions requires an exact rendered fragment.
func assertBlockMentions(t *testing.T, block, needle, what string) {
	t.Helper()
	if !strings.Contains(block, needle) {
		t.Fatalf("%s must mention %q, block was:\n%s", what, needle, block)
	}
}

func resolvedForProfile(path string) string {
	return fspath.CanonicalPath(path)
}

// escapeAssertWriteLands runs a confined touch of path and requires it to
// succeed and leave the file behind: the positive control for a denial.
func escapeAssertWriteLands(t *testing.T, self string, c confine.Confinement, path string) {
	t.Helper()
	if code, out := confinedRun(t, self, c, "/usr/bin/touch", path); code != 0 {
		t.Fatalf("positive control: write of %s must succeed, exit=%d out=%s", path, code, out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("positive control: %s missing after the write: %v", path, err)
	}
}

// escapeAssertWriteRefused runs a confined touch of path and requires a
// refusal that leaves no file behind.
func escapeAssertWriteRefused(t *testing.T, self string, c confine.Confinement, path string) {
	t.Helper()
	if code := confinedExit(t, self, c, "/usr/bin/touch", path); code == 0 {
		t.Errorf("confined write of %s must be refused", path)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s exists after the refused write (err %v)", path, err)
	}
}

// The bearer token in the state tree is unreadable while drafts and the
// browser cache beside it stay readable.
func TestBoundaryConfigTokenUnreadable(t *testing.T) {
	self := requireLiveBoundary(t)
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	const bearer = "escape-bearer-sentinel"
	tokenPath := filepath.Join(cfg, "api.token")
	testutil.FailErr(t, "write api.token", os.WriteFile(tokenPath, []byte(bearer), 0o600))
	draftFile := filepath.Join(cfg, "drafts", "escape-f1", "ok.txt")
	browserFile := filepath.Join(cfg, "browser-cache", "ok.bin")
	for path, body := range map[string]string{draftFile: "draft-ok", browserFile: "chrome-ok"} {
		testutil.FailErr(t, "mkdir fixture", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write fixture", os.WriteFile(path, []byte(body), 0o600))
	}

	proj := t.TempDir()
	c := confine.Confinement{Roots: []string{proj}}
	code, out := confinedRun(t, self, c, "/bin/cat", tokenPath)
	if code == 0 || strings.Contains(out, bearer) {
		t.Fatalf("confined read of api.token must be refused: exit=%d out=%q", code, out)
	}
	for path, body := range map[string]string{draftFile: "draft-ok", browserFile: "chrome-ok"} {
		if code, out := confinedRun(t, self, c, "/bin/cat", path); code != 0 || out != body {
			t.Errorf("positive control: %s must stay readable, exit=%d out=%q", path, code, out)
		}
	}
	escapeAssertWriteLands(t, self, c, filepath.Join(proj, "escape-suite-ok.txt"))
}

// Profiles stay in memory to prevent cross-command replacement: no argument
// names a profile file and launching stages nothing in the temporary folder.
func TestProfileIsNotStagedInAFile(t *testing.T) {
	self := requireLiveBoundary(t)
	root := t.TempDir()
	c := confine.Confinement{Roots: []string{root}, Network: confine.NetworkDeny}
	staged := func(name string) bool {
		return strings.HasPrefix(name, "lycaon-sbpl-") || strings.HasSuffix(name, ".sb")
	}
	tmp := os.TempDir()
	before := map[string]bool{}
	entries, err := os.ReadDir(tmp)
	testutil.FailErr(t, "read temporary folder", err)
	for _, e := range entries {
		before[e.Name()] = true
	}

	cmd, cleanup, err := confine.Command(context.Background(), self, "true", nil, c)
	testutil.FailErr(t, "confine.Command failed", err)
	defer cleanup()
	for _, arg := range cmd.Args {
		if staged(filepath.Base(arg)) {
			t.Fatalf("profile passed as a file path: %v", cmd.Args)
		}
	}
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("profile must travel over one inherited pipe, got %d extra files", len(cmd.ExtraFiles))
	}
	after, err := os.ReadDir(tmp)
	testutil.FailErr(t, "read temporary folder", err)
	for _, e := range after {
		if staged(e.Name()) && !before[e.Name()] {
			t.Fatalf("launching staged a profile file in the temporary folder: %s", e.Name())
		}
	}

	inside := filepath.Join(root, "probe.txt")
	if code, out := confinedRun(t, self, c, "sh", "-c", "echo hi > "+shellSingleQuote(inside)); code != 0 {
		t.Fatalf("positive control: shell write inside the jail must succeed, exit=%d out=%s", code, out)
	}
	if got, err := os.ReadFile(inside); err != nil || string(got) != "hi\n" {
		t.Fatalf("positive control: %s = %q (err %v), want the written line", inside, got, err)
	}
	outside := "/etc/lycaon-escape-probe"
	if code := confinedExit(t, self, c, "sh", "-c", "echo hi > "+outside); code == 0 {
		t.Fatal("write outside the jail should be denied")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("%s exists after the refused write (err %v)", outside, err)
	}
}

// escapeForeignTemporaryTree is a directory beside the write roots: another
// user's /private/var/folders subtree on macOS, and on Linux, whose temporary
// roots are shared by name, a directory in the home tree outside them.
func escapeForeignTemporaryTree(t *testing.T) string {
	t.Helper()
	parent, err := os.UserHomeDir()
	testutil.FailErr(t, "resolve home", err)
	if runtime.GOOS == "darwin" {
		systemTmp, err := exec.Command("/usr/bin/getconf", "DARWIN_USER_TEMP_DIR").Output()
		testutil.FailErr(t, "resolve system temporary directory", err)
		parent = filepath.Dir(filepath.Dir(strings.TrimSpace(string(systemTmp))))
	}
	foreign, err := os.MkdirTemp(parent, "paintedwolf-escape-foreign-") //nolint:usetesting // Probe must be outside this process's temporary subtree.
	testutil.FailErr(t, "create foreign temporary subtree", err)
	t.Cleanup(func() { _ = os.RemoveAll(foreign) })
	return foreign
}

// A write under a foreign temporary subtree is refused while the process's
// own temporary folder and the project stay writable.
func TestBoundaryForeignTemporaryTreeWriteDenied(t *testing.T) {
	self := requireLiveBoundary(t)
	proj := t.TempDir()
	c := confine.Confinement{Roots: []string{proj}}

	escapeAssertWriteRefused(t, self, c, filepath.Join(escapeForeignTemporaryTree(t), "escape.txt"))
	escapeAssertWriteLands(t, self, c, filepath.Join(proj, "escape-suite-ok.txt"))
	ownTmp := filepath.Join(os.TempDir(), "lycaon-escape-f6-own.txt")
	_ = os.Remove(ownTmp)
	t.Cleanup(func() { _ = os.Remove(ownTmp) })
	escapeAssertWriteLands(t, self, c, ownTmp)
}

// A confined program of a project with editor-only roots writes in its
// admitted root and is refused in each attached editor-only root.
func TestConfinedLaunchStaysOutOfEditorOnlyRoots(t *testing.T) {
	self := requireLiveBoundary(t)
	facts := rootfacts.Install(t)
	params := project.CreateParams{Roots: []project.AttachRootParams{{Path: facts.Work}}}
	for _, root := range facts.EditorOnly() {
		params.Roots = append(params.Roots, project.AttachRootParams{Path: root.Path})
	}
	p, err := project.NewMemoryRegistry().Create(t.Context(), params)
	testutil.FailErr(t, "create project", err)
	c := confine.Confinement{Roots: project.RootPaths(p)}
	escapeAssertWriteLands(t, self, c, filepath.Join(facts.Work, "agent-write.txt"))
	for _, root := range facts.EditorOnly() {
		t.Run(root.Name, func(t *testing.T) {
			escapeAssertWriteRefused(t, self, c, filepath.Join(root.Path, "agent-write.txt"))
		})
	}
}
