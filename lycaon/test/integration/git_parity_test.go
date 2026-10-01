package integration

import (
	"bytes"
	"errors"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/testutil"
)

const envGitReference = "LYCAON_GIT_REFERENCE"
const envParityRequired = "LYCAON_GIT_PARITY_REQUIRED"

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

func requireReferenceGit(t *testing.T) string {
	t.Helper()
	ref := strings.TrimSpace(os.Getenv(envGitReference))
	if ref == "" {
		if parityRequired() {
			t.Fatal("set LYCAON_GIT_REFERENCE to run git parity")
		}
		t.Skip("set LYCAON_GIT_REFERENCE to run git parity")
	}
	st, err := os.Stat(ref)
	if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		t.Fatalf("%s=%q is not executable", envGitReference, ref)
	}
	return ref
}

func requireBundledEngine(t *testing.T) {
	t.Helper()
	if _, err := gitengine.BinaryPath(); err != nil {
		if parityRequired() {
			t.Fatalf("bundled git required for parity: %v (run ./task gitengine:fetch)", err)
		}
		t.Skip("bundled git missing — run ./task gitengine:fetch")
	}
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
}

func parityRequired() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(envParityRequired)))
	return v == "1" || v == "true" || v == "yes"
}

func skipIfShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: skipped under -short")
	}
}

func refRun(t *testing.T, ref, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := osexec.CommandContext(t.Context(), ref, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ref git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func refRunAllowFail(t *testing.T, ref, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := osexec.CommandContext(t.Context(), ref, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *osexec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("ref git %v: %v\n%s", args, err, out)
		}
	}
	return string(out), code
}

func fakeHomeEnv(t *testing.T) (home string, env []string) {
	t.Helper()
	home = t.TempDir()
	env = []string{
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Fixture Builder",
		"GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture Builder",
		"GIT_COMMITTER_EMAIL=fixture@example.com",
	}
	return home, env
}

func writeFileBytes(t *testing.T, dir, rel string, body []byte) {
	t.Helper()
	path := filepath.Join(dir, rel)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write", os.WriteFile(path, body, 0o644))
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+path, err)
	return raw
}

func resolveLFSForFixtures(t *testing.T) string {
	t.Helper()
	if p, err := gitengine.LFSPath(); err == nil {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	if p, err := osexec.LookPath("git-lfs"); err == nil {
		return p
	}
	t.Skip("git-lfs not available for fixture construction")
	return ""
}

func lfsConfigArgs(lfsPath string) []string {
	return []string{
		"-c", "filter.lfs.clean=" + lfsPath + " clean -- %f",
		"-c", "filter.lfs.smudge=" + lfsPath + " smudge -- %f",
		"-c", "filter.lfs.process=" + lfsPath + " filter-process",
		"-c", "filter.lfs.required=true",
	}
}

type fixtureKind string

const (
	fixPlain            fixtureKind = "plain"
	fixLFS              fixtureKind = "lfs"
	fixAttributesFilter fixtureKind = "attributes-filter"
	fixSubmodule        fixtureKind = "submodule"
	fixEOL              fixtureKind = "eol"
	fixLargeFile        fixtureKind = "large-file"
)

type fixture struct {
	kind    fixtureKind
	dir     string
	home    string
	branch2 string
}

func buildFixture(t *testing.T, ref string, kind fixtureKind) fixture {
	t.Helper()
	home, env := fakeHomeEnv(t)
	root := t.TempDir()
	dir := filepath.Join(root, "repo")
	testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))

	f := fixture{kind: kind, dir: dir, home: home, branch2: "topic"}

	switch kind {
	case fixPlain:
		buildPlain(t, ref, dir, env)
	case fixLFS:
		buildLFS(t, ref, dir, env, root)
	case fixAttributesFilter:
		buildAttributesFilter(t, ref, dir, env)
	case fixSubmodule:
		buildSubmodule(t, ref, dir, env, root)
	case fixEOL:
		buildEOL(t, ref, dir, env)
	case fixLargeFile:
		buildLargeFile(t, ref, dir, env)
	default:
		t.Fatalf("unknown fixture %q", kind)
	}

	cfg := filepath.Join(home, ".gitconfig")
	if _, err := os.Stat(cfg); os.IsNotExist(err) {
		testutil.FailErr(t, "seed empty gitconfig", os.WriteFile(cfg, []byte{}, 0o644))
	}
	return f
}

func buildPlain(t *testing.T, ref, dir string, env []string) {
	t.Helper()
	refRun(t, ref, dir, env, "init")
	refRun(t, ref, dir, env, "checkout", "-b", "main")
	writeFileBytes(t, dir, "readme.txt", []byte("hello\n"))
	refRun(t, ref, dir, env, "add", "readme.txt")
	refRun(t, ref, dir, env, "commit", "-m", "init")
	writeFileBytes(t, dir, "readme.txt", []byte("hello world\n"))
	refRun(t, ref, dir, env, "add", "readme.txt")
	refRun(t, ref, dir, env, "commit", "-m", "update")
	refRun(t, ref, dir, env, "checkout", "-b", "topic")
	writeFileBytes(t, dir, "topic.txt", []byte("topic\n"))
	refRun(t, ref, dir, env, "add", "topic.txt")
	refRun(t, ref, dir, env, "commit", "-m", "topic commit")
	refRun(t, ref, dir, env, "checkout", "main")
	writeFileBytes(t, dir, "staged.txt", []byte("staged\n"))
	refRun(t, ref, dir, env, "add", "staged.txt")
	writeFileBytes(t, dir, "unstaged.txt", []byte("unstaged\n"))
	writeFileBytes(t, dir, "untracked.txt", []byte("untracked\n"))
}

func buildLFS(t *testing.T, ref, dir string, env []string, root string) {
	t.Helper()
	lfs := resolveLFSForFixtures(t)
	cfg := lfsConfigArgs(lfs)
	run := func(args ...string) {
		t.Helper()
		refRun(t, ref, dir, env, append(append([]string{}, cfg...), args...)...)
	}

	run("init")
	run("checkout", "-b", "main")
	writeFileBytes(t, dir, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	run("add", ".gitattributes")
	run("commit", "-m", "attrs")

	payload := bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 256)
	writeFileBytes(t, dir, "blob.bin", payload)
	run("add", "blob.bin")
	run("commit", "-m", "lfs blob")

	remote := filepath.Join(root, "remote.git")
	refRun(t, ref, root, env, append(append([]string{}, cfg...), "clone", "--bare", dir, remote)...)
	run("remote", "add", "origin", remote)

	run("checkout", "-b", "topic")
	writeFileBytes(t, dir, "note.txt", []byte("note\n"))
	run("add", "note.txt")
	run("commit", "-m", "topic note")
	run("checkout", "main")
}

func buildAttributesFilter(t *testing.T, ref, dir string, env []string) {
	t.Helper()
	refRun(t, ref, dir, env, "init")
	refRun(t, ref, dir, env, "checkout", "-b", "main")
	writeFileBytes(t, dir, ".gitattributes", []byte("*.dat filter=custom-undefined\n"))
	writeFileBytes(t, dir, "data.dat", []byte("plain data\n"))
	refRun(t, ref, dir, env, "add", "-A")
	refRun(t, ref, dir, env, "commit", "-m", "undefined filter")
	refRun(t, ref, dir, env, "checkout", "-b", "topic")
	writeFileBytes(t, dir, "more.txt", []byte("more\n"))
	refRun(t, ref, dir, env, "add", "more.txt")
	refRun(t, ref, dir, env, "commit", "-m", "topic")
	refRun(t, ref, dir, env, "checkout", "main")
}

func buildSubmodule(t *testing.T, ref, dir string, env []string, root string) {
	t.Helper()
	sub := filepath.Join(root, "sub")
	testutil.FailErr(t, "mkdir sub", os.MkdirAll(sub, 0o755))
	refRun(t, ref, sub, env, "init")
	refRun(t, ref, sub, env, "checkout", "-b", "main")
	writeFileBytes(t, sub, "sub.txt", []byte("sub\n"))
	refRun(t, ref, sub, env, "add", "sub.txt")
	refRun(t, ref, sub, env, "commit", "-m", "sub init")

	refRun(t, ref, dir, env, "init")
	refRun(t, ref, dir, env, "checkout", "-b", "main")
	writeFileBytes(t, dir, "root.txt", []byte("root\n"))
	refRun(t, ref, dir, env, "add", "root.txt")
	refRun(t, ref, dir, env, "commit", "-m", "root init")
	refRun(t, ref, dir, env, "-c", "protocol.file.allow=always", "submodule", "add", sub, "vendor")
	refRun(t, ref, dir, env, "commit", "-m", "add submodule")
	refRun(t, ref, dir, env, "checkout", "-b", "topic")
	writeFileBytes(t, dir, "extra.txt", []byte("extra\n"))
	refRun(t, ref, dir, env, "add", "extra.txt")
	refRun(t, ref, dir, env, "commit", "-m", "topic")
	refRun(t, ref, dir, env, "checkout", "main")
}

func buildEOL(t *testing.T, ref, dir string, env []string) {
	t.Helper()
	refRun(t, ref, dir, env, "init")
	refRun(t, ref, dir, env, "checkout", "-b", "main")
	writeFileBytes(t, dir, ".gitattributes", []byte("*.txt text eol=lf\n"))
	writeFileBytes(t, dir, "lines.txt", []byte("one\r\ntwo\r\nthree\r\n"))
	refRun(t, ref, dir, env, "add", "-A")
	refRun(t, ref, dir, env, "commit", "-m", "eol")
	refRun(t, ref, dir, env, "checkout", "-b", "topic")
	writeFileBytes(t, dir, "other.txt", []byte("x\r\ny\r\n"))
	refRun(t, ref, dir, env, "add", "other.txt")
	refRun(t, ref, dir, env, "commit", "-m", "topic eol")
	refRun(t, ref, dir, env, "checkout", "main")
}

func buildLargeFile(t *testing.T, ref, dir string, env []string) {
	t.Helper()
	refRun(t, ref, dir, env, "init")
	refRun(t, ref, dir, env, "checkout", "-b", "main")
	const size = 50 << 20
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(i % 251)
	}
	writeFileBytes(t, dir, "big.bin", buf)
	refRun(t, ref, dir, env, "add", "big.bin")
	refRun(t, ref, dir, env, "commit", "-m", "large")
	refRun(t, ref, dir, env, "checkout", "-b", "topic")
	writeFileBytes(t, dir, "small.txt", []byte("ok\n"))
	refRun(t, ref, dir, env, "add", "small.txt")
	refRun(t, ref, dir, env, "commit", "-m", "topic")
	refRun(t, ref, dir, env, "checkout", "main")
}

func normalizeOutput(raw string, tempRoots []string) string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	for _, root := range tempRoots {
		if root == "" {
			continue
		}
		s = strings.ReplaceAll(s, root, "$TMP")
		s = strings.ReplaceAll(s, filepath.Clean(root), "$TMP")
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

func normalizeSorted(raw string, tempRoots []string) string {
	s := normalizeOutput(raw, tempRoots)
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func engineRun(t *testing.T, dir string, args []string, opts gitexec.Opts) (string, int) {
	t.Helper()
	if opts.Timeout <= 0 {
		opts.Timeout = exec.DefaultGitTimeout
	}
	if opts.MaxOutput <= 0 {
		opts.MaxOutput = int64(exec.DefaultMaxOutputBytes)
	}
	out, code, err := gitexec.Run(t.Context(), dir, args, opts)
	if err != nil && code == -1 {
		t.Fatalf("gitexec %v: %v", args, err)
	}
	return string(out), code
}

func compareOp(t *testing.T, name, engineOut, refOut string, sortLines bool, tempRoots []string) {
	t.Helper()
	var a, b string
	if sortLines {
		a = normalizeSorted(engineOut, tempRoots)
		b = normalizeSorted(refOut, tempRoots)
	} else {
		a = normalizeOutput(engineOut, tempRoots)
		b = normalizeOutput(refOut, tempRoots)
	}
	if a != b {
		t.Fatalf("%s mismatch:\n--- engine ---\n%s\n--- reference ---\n%s", name, a, b)
	}
}

func listTrackedFiles(t *testing.T, ref, dir string, env []string) []string {
	t.Helper()
	out := refRun(t, ref, dir, env, "ls-files", "-z")
	parts := strings.Split(out, "\x00")
	var files []string
	for _, p := range parts {
		if p == "" {
			continue
		}
		files = append(files, p)
	}
	sort.Strings(files)
	return files
}

func copyDirIfExists(t *testing.T, src, dst string) {
	t.Helper()
	if _, err := os.Stat(src); err != nil {
		return
	}
	copyTree(t, src, dst)
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	testutil.FailErr(t, "mkdir", os.MkdirAll(dst, 0o755))
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		// Preserve symlinks (e.g. submodule gitdirs).
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
	testutil.FailErr(t, "copy tree", err)
}

func runParityOpsIsolated(t *testing.T, ref string, src fixture) {
	t.Helper()
	root := t.TempDir()
	engineDir := filepath.Join(root, "engine")
	refDir := filepath.Join(root, "ref")
	env := []string{"HOME=" + src.home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}

	// Filesystem copy preserves staged/unstaged/untracked; clone would not.
	copyTree(t, src.dir, engineDir)
	copyTree(t, src.dir, refDir)

	// Reference "default environment" still needs LFS drivers for an LFS repo —
	// users get those via `git lfs install`; we pass them as transient -c so the
	// fake HOME never receives a config write.
	var refPrefix []string
	if src.kind == fixLFS {
		refPrefix = lfsConfigArgs(resolveLFSForFixtures(t))
		refRun(t, ref, refDir, env, append(append([]string{}, refPrefix...), "checkout", "-f", "HEAD")...)
		out, code := engineRun(t, engineDir, []string{"checkout", "-f", "HEAD"}, gitexec.Opts{Timeout: 2 * time.Minute})
		if code != 0 {
			t.Fatalf("engine LFS checkout: code=%d out=%s", code, out)
		}
	}

	tempRoots := []string{engineDir, refDir, src.dir, src.home, root}
	opts := gitexec.Opts{}
	if src.kind == fixLargeFile {
		opts.MaxOutput = 8 << 20
		opts.Timeout = 5 * time.Minute
	}
	if src.kind == fixLFS {
		opts.Timeout = 2 * time.Minute
	}

	refArgs := func(args ...string) []string {
		if len(refPrefix) == 0 {
			return args
		}
		return append(append([]string{}, refPrefix...), args...)
	}

	type op struct {
		name string
		args []string
		sort bool
	}
	ops := []op{
		{"status", []string{"status", "--porcelain=v2"}, true},
		{"log", []string{"log", "--format=%H%x09%s", "-n", "20"}, false},
		{"diff-stat", []string{"diff", "--stat"}, false},
		{"rev-parse", []string{"rev-parse", "HEAD"}, false},
		{"ls-files", []string{"ls-files", "-s"}, true},
	}

	for _, o := range ops {
		eOut, eCode := engineRun(t, engineDir, o.args, opts)
		rOut, rCode := refRunAllowFail(t, ref, refDir, env, refArgs(o.args...)...)
		if eCode != rCode {
			t.Fatalf("%s: exit engine=%d ref=%d\nengine=%s\nref=%s", o.name, eCode, rCode, eOut, rOut)
		}
		compareOp(t, o.name, eOut, rOut, o.sort, tempRoots)
	}

	eOut, eCode := engineRun(t, engineDir, []string{"checkout", src.branch2}, opts)
	if eCode != 0 {
		t.Fatalf("engine checkout: code=%d out=%s", eCode, eOut)
	}
	rOut, rCode := refRunAllowFail(t, ref, refDir, env, refArgs("checkout", src.branch2)...)
	if rCode != 0 {
		t.Fatalf("ref checkout: code=%d out=%s", rCode, rOut)
	}

	eOut, eCode = engineRun(t, engineDir, []string{"status", "--porcelain=v2"}, opts)
	rOut, rCode = refRunAllowFail(t, ref, refDir, env, refArgs("status", "--porcelain=v2")...)
	if eCode != rCode {
		t.Fatalf("post-checkout status exit engine=%d ref=%d", eCode, rCode)
	}
	compareOp(t, "post-checkout-status", eOut, rOut, true, tempRoots)

	files := listTrackedFiles(t, ref, refDir, env)
	for _, rel := range files {
		ep := filepath.Join(engineDir, rel)
		rp := filepath.Join(refDir, rel)
		es, err := os.Stat(ep)
		if err != nil {
			t.Fatalf("engine stat %s: %v", rel, err)
		}
		rs, err := os.Stat(rp)
		if err != nil {
			t.Fatalf("ref stat %s: %v", rel, err)
		}
		if es.IsDir() || rs.IsDir() {
			continue
		}
		if !bytes.Equal(readFileBytes(t, ep), readFileBytes(t, rp)) {
			t.Fatalf("working-tree content mismatch for %s", rel)
		}
	}
}

func TestGitParityMatrix(t *testing.T) {
	skipIfShort(t)
	ref := requireReferenceGit(t)
	requireBundledEngine(t)

	kinds := []fixtureKind{
		fixPlain, fixLFS, fixAttributesFilter, fixSubmodule, fixEOL, fixLargeFile,
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			f := buildFixture(t, ref, kind)
			runParityOpsIsolated(t, ref, f)
		})
	}
}

func TestLFSSmudgeWithoutInstall(t *testing.T) {
	skipIfShort(t)
	ref := requireReferenceGit(t)
	requireBundledEngine(t)
	if p, err := gitengine.LFSPath(); err != nil {
		t.Skip("bundled git-lfs missing")
	} else if st, err := os.Stat(p); err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		t.Skip("bundled git-lfs missing")
	}

	f := buildFixture(t, ref, fixLFS)
	cfgPath := filepath.Join(f.home, ".gitconfig")
	before := readFileBytes(t, cfgPath)

	destRoot := t.TempDir()
	dest := filepath.Join(destRoot, "clone")
	t.Setenv("HOME", f.home)

	out, code, err := gitexec.Run(t.Context(), destRoot, []string{
		"clone", "--local", f.dir, dest,
	}, gitexec.Opts{Profile: gitexec.ProfileNetwork, Timeout: 2 * time.Minute})
	if err != nil || code != 0 {
		t.Logf("clone via gitexec: err=%v code=%d out=%s — copying tree instead", err, code, out)
		copyTree(t, f.dir, dest)
	} else {
		copyDirIfExists(t, filepath.Join(f.dir, ".git", "lfs"), filepath.Join(dest, ".git", "lfs"))
	}

	out, code, err = gitexec.Run(t.Context(), dest, []string{"checkout", "-f", "HEAD"}, gitexec.Opts{
		Timeout: 2 * time.Minute,
	})
	if err != nil || code != 0 {
		t.Fatalf("checkout: err=%v code=%d out=%s", err, code, out)
	}

	got := readFileBytes(t, filepath.Join(dest, "blob.bin"))
	if bytes.HasPrefix(got, []byte("version https://git-lfs.github.com/spec/v1")) {
		t.Fatalf("got LFS pointer instead of smudged content:\n%s", got)
	}
	want := bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 256)
	if !bytes.Equal(got, want) {
		t.Fatalf("blob.bin content mismatch: len=%d want=%d", len(got), len(want))
	}

	after := readFileBytes(t, cfgPath)
	if !bytes.Equal(before, after) {
		t.Fatalf("~/.gitconfig mutated by LFS smudge:\nbefore=%q\nafter=%q", before, after)
	}
}

// Git filter values require quoting when the bundled engine path contains spaces.
func TestLFSFromEnginePathWithSpaces(t *testing.T) {
	skipIfShort(t)
	requireBundledEngine(t)
	realLFS, err := gitengine.LFSPath()
	if err != nil {
		t.Skip("bundled git-lfs missing")
	}
	if st, statErr := os.Stat(realLFS); statErr != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		t.Skip("bundled git-lfs missing")
	}

	spaced := filepath.Join(t.TempDir(), "Painted Wolf Code.app", "bin")
	testutil.FailErr(t, "mkdir spaced engine dir", os.MkdirAll(spaced, 0o755))
	stagedLFS := filepath.Join(spaced, "git-lfs")
	testutil.FailErr(t, "stage git-lfs", os.WriteFile(stagedLFS, readFileBytes(t, realLFS), 0o755))
	t.Cleanup(gitexec.TestingUseLFSPath(stagedLFS))

	dir := t.TempDir()
	out, code, err := gitexec.Run(t.Context(), dir, []string{"init"}, gitexec.Opts{})
	if err != nil || code != 0 {
		t.Fatalf("init: err=%v code=%d out=%s", err, code, out)
	}
	writeFileBytes(t, dir, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	content := bytes.Repeat([]byte{0x0a, 0x0b, 0x0c, 0x0d}, 256)
	writeFileBytes(t, dir, "blob.bin", content)

	out, code, err = gitexec.Run(t.Context(), dir, []string{"add", "-A"}, gitexec.Opts{})
	if err != nil || code != 0 {
		t.Fatalf("add through a spaced engine path: err=%v code=%d out=%s", err, code, out)
	}
	out, code, err = gitexec.Run(t.Context(), dir, []string{"commit", "-m", "lfs"}, gitexec.Opts{
		Identity: &gitexec.Identity{Name: "Test User", Email: "test@example.com"},
	})
	if err != nil || code != 0 {
		t.Fatalf("commit: err=%v code=%d out=%s", err, code, out)
	}

	// The staged blob must be a pointer (the clean filter ran) …
	out, code, err = gitexec.Run(t.Context(), dir, []string{"show", "HEAD:blob.bin"}, gitexec.Opts{})
	if err != nil || code != 0 {
		t.Fatalf("show: err=%v code=%d out=%s", err, code, out)
	}
	if !bytes.HasPrefix(out, []byte("version https://git-lfs.github.com/spec/v1")) {
		t.Fatalf("clean filter did not run through the spaced path: %q", out)
	}

	// … and a forced checkout must smudge it back to the real bytes.
	testutil.FailErr(t, "remove worktree copy", os.Remove(filepath.Join(dir, "blob.bin")))
	out, code, err = gitexec.Run(t.Context(), dir, []string{"checkout", "-f", "HEAD"}, gitexec.Opts{})
	if err != nil || code != 0 {
		t.Fatalf("checkout: err=%v code=%d out=%s", err, code, out)
	}
	if got := readFileBytes(t, filepath.Join(dir, "blob.bin")); !bytes.Equal(got, content) {
		t.Fatalf("smudge through a spaced path returned %d bytes, want %d", len(got), len(content))
	}
}

func TestUndefinedFilterIsNoOp(t *testing.T) {
	skipIfShort(t)
	ref := requireReferenceGit(t)
	requireBundledEngine(t)

	f := buildFixture(t, ref, fixAttributesFilter)
	out, code, err := gitexec.Run(t.Context(), f.dir, []string{"checkout", "-f", "HEAD"}, gitexec.Opts{})
	if err != nil || code != 0 {
		t.Fatalf("checkout with undefined filter: err=%v code=%d out=%s", err, code, out)
	}
	got := string(readFileBytes(t, filepath.Join(f.dir, "data.dat")))
	if got != "plain data\n" {
		t.Fatalf("data.dat = %q, want plain data", got)
	}
}

func TestRepoAttributesStillApply(t *testing.T) {
	skipIfShort(t)
	ref := requireReferenceGit(t)
	requireBundledEngine(t)

	f := buildFixture(t, ref, fixEOL)
	root := t.TempDir()
	engineDir := filepath.Join(root, "engine")
	refDir := filepath.Join(root, "ref")
	env := []string{"HOME=" + f.home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
	refRun(t, ref, root, env, "clone", "--local", f.dir, engineDir)
	refRun(t, ref, root, env, "clone", "--local", f.dir, refDir)

	out, code, err := gitexec.Run(t.Context(), engineDir, []string{"checkout", "-f", "HEAD"}, gitexec.Opts{})
	if err != nil || code != 0 {
		t.Fatalf("engine checkout: err=%v code=%d out=%s", err, code, out)
	}
	refRun(t, ref, refDir, env, "checkout", "-f", "HEAD")

	e := readFileBytes(t, filepath.Join(engineDir, "lines.txt"))
	r := readFileBytes(t, filepath.Join(refDir, "lines.txt"))
	if !bytes.Equal(e, r) {
		t.Fatalf("eol mismatch (core.attributesFile=/dev/null must not disable repo attrs):\nengine=%q\nref=%q", e, r)
	}
	if bytes.Contains(e, []byte("\r\n")) {
		t.Fatalf("expected LF endings after eol=lf attributes, got CRLF: %q", e)
	}
}

func TestLargeFileWithinCaps(t *testing.T) {
	skipIfShort(t)
	ref := requireReferenceGit(t)
	requireBundledEngine(t)

	f := buildFixture(t, ref, fixLargeFile)
	opts := gitexec.Opts{
		MaxOutput: int64(exec.DefaultMaxOutputBytes),
		Timeout:   5 * time.Minute,
	}
	out, code, err := gitexec.Run(t.Context(), f.dir, []string{"status", "--porcelain=v2"}, opts)
	if err != nil {
		if strings.Contains(err.Error(), "truncat") || strings.Contains(string(out), "truncat") {
			t.Fatalf("output truncated: %v\n%s", err, out)
		}
		t.Fatalf("status: %v code=%d out=%s", err, code, out)
	}
	if code != 0 {
		t.Fatalf("status code=%d out=%s", code, out)
	}
	out, code, err = gitexec.Run(t.Context(), f.dir, []string{"rev-parse", "HEAD"}, opts)
	if err != nil || code != 0 {
		t.Fatalf("rev-parse: err=%v code=%d out=%s", err, code, out)
	}
	out, code, err = gitexec.Run(t.Context(), f.dir, []string{"ls-files", "-s"}, opts)
	if err != nil || code != 0 {
		t.Fatalf("ls-files: err=%v code=%d out=%s", err, code, out)
	}
}

func TestLFSAbsentDegradesGracefully(t *testing.T) {
	skipIfShort(t)
	ref := requireReferenceGit(t)
	requireBundledEngine(t)

	f := buildFixture(t, ref, fixPlain)
	t.Setenv(gitengine.EnvTest, "1")
	t.Setenv(gitexec.EnvLFSForceAbsent, "1")

	ops := [][]string{
		{"status", "--porcelain=v2"},
		{"log", "--format=%H%x09%s", "-n", "5"},
		{"diff", "--stat"},
		{"rev-parse", "HEAD"},
		{"ls-files", "-s"},
	}
	for _, args := range ops {
		out, code, err := gitexec.Run(t.Context(), f.dir, args, gitexec.Opts{})
		if err != nil || code != 0 {
			t.Fatalf("%v with LFS absent: err=%v code=%d out=%s", args, err, code, out)
		}
	}
}
