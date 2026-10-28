//go:build darwin || linux

package literal

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// duFiles is a small project tree: nested directories, a hidden file, a file
// large enough to span several blocks and an empty directory.
var duFiles = map[string]string{
	"proj/a.txt":           "alpha\n",
	"proj/.env":            "SECRET=1\n",
	"proj/src/main.go":     strings.Repeat("package main\n", 900),
	"proj/src/lib/util.go": "package lib\n",
	"proj/empty/":          "",
}

// diskUsage sums an independent walk with du's rules: every entry's
// allocated 512-byte blocks, or each file's byte size; hard links once.
func diskUsage(t *testing.T, root string, apparent bool) int64 {
	t.Helper()
	var total int64
	seen := map[[2]uint64]bool{}
	testutil.FailErr(t, "walk "+root, filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() && st.Nlink > 1 {
			key := [2]uint64{uint64(st.Dev), uint64(st.Ino)}
			if seen[key] {
				return nil
			}
			seen[key] = true
		}
		if apparent {
			if !info.IsDir() {
				total += info.Size()
			}
		} else {
			total += int64(st.Blocks) * 512
		}
		return nil
	}))
	return total
}

func TestDuSummariesMatchBlockAndByteSums(t *testing.T) {
	h := newHarness(t, duFiles)
	h.cwd = "proj"
	root := filepath.Join(h.root, "proj")
	blocks, bytes := diskUsage(t, root, false), diskUsage(t, root, true)
	kib := func(n int64) string { return strconv.FormatInt((n+1023)/1024, 10) }
	for _, c := range []formCase{
		{argv: "du -sh .", stdout: humanSize(blocks) + "\t.\n"},
		{argv: "du -s .", stdout: kib(blocks) + "\t.\n"},
		{argv: "du -sk .", stdout: kib(blocks) + "\t.\n"},
		{argv: "du --summarize --human-readable .", stdout: humanSize(blocks) + "\t.\n"},
		{argv: "du -s --apparent-size .", stdout: kib(bytes) + "\t.\n"},
		{argv: "du -sA .", stdout: kib(bytes) + "\t.\n"},
		{argv: "du -sb .", stdout: strconv.FormatInt(bytes, 10) + "\t.\n"},
		{argv: "du -s --apparent-size -h .", stdout: humanSize(bytes) + "\t.\n"},
		{argv: "du -sm .", stdout: strconv.FormatInt((blocks+1<<20-1)>>20, 10) + "\t.\n"},
		{argv: "du -s -h -k .", stdout: kib(blocks) + "\t.\n"},
		{argv: "du -d 0 .", stdout: kib(blocks) + "\t.\n"},
		{argv: "du -s", stdout: kib(blocks) + "\t.\n"},
		{argv: "du -b a.txt", stdout: "6\ta.txt\n"},
		{argv: "du -cb a.txt src/lib/util.go", stdout: "6\ta.txt\n12\tsrc/lib/util.go\n18\ttotal\n"},
	} {
		t.Run(c.argv, func(t *testing.T) {
			h.exec(c.stdin, strings.Fields(c.argv)...).expect(t, c.argv, c.stdout, c.stderr, c.status)
		})
	}
}

// duLines runs du and returns its (size, path) lines in order.
func duLines(t *testing.T, h *harness, argv ...string) [][2]string {
	t.Helper()
	r := h.exec("", argv...)
	r.expect(t, strings.Join(argv, " "), r.stdout, "", 0)
	var out [][2]string
	for _, line := range strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n") {
		size, path, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("du line %q", line)
		}
		out = append(out, [2]string{size, path})
	}
	return out
}

func paths(lines [][2]string) string {
	var out []string
	for _, line := range lines {
		out = append(out, line[1])
	}
	return strings.Join(out, " ")
}

// TestDuWalksPostOrderAndSelectsDepth: directories print after their
// contents in byte order; -a adds files; -d limits printed depth while
// totals still cover everything below.
func TestDuWalksPostOrderAndSelectsDepth(t *testing.T) {
	h := newHarness(t, duFiles)
	for _, c := range []struct{ argv, paths string }{
		{argv: "du proj", paths: "proj/empty proj/src/lib proj/src proj"},
		{argv: "du -a proj", paths: "proj/.env proj/a.txt proj/empty proj/src/lib/util.go proj/src/lib proj/src/main.go proj/src proj"},
		{argv: "du --all proj/", paths: "proj/.env proj/a.txt proj/empty proj/src/lib/util.go proj/src/lib proj/src/main.go proj/src proj/"},
		{argv: "du -d 1 proj", paths: "proj/empty proj/src proj"},
		{argv: "du --max-depth=1 -a proj", paths: "proj/.env proj/a.txt proj/empty proj/src proj"},
		{argv: "du --max-depth 0 proj", paths: "proj"},
		{argv: "du -c proj/src proj/a.txt", paths: "proj/src/lib proj/src proj/a.txt total"},
		{argv: "du -x proj/src", paths: "proj/src/lib proj/src"},
	} {
		if got := paths(duLines(t, h, strings.Fields(c.argv)...)); got != c.paths {
			t.Errorf("%s printed %q, want %q", c.argv, got, c.paths)
		}
	}
	lines := duLines(t, h, "du", "-ab", "proj")
	sizes := map[string]int64{}
	for _, line := range lines {
		n, err := strconv.ParseInt(line[0], 10, 64)
		testutil.FailErr(t, "size "+line[1], err)
		sizes[line[1]] = n
	}
	for name, content := range duFiles {
		if !strings.HasSuffix(name, "/") && sizes[name] != int64(len(content)) {
			t.Errorf("du -ab %s = %d, want %d", name, sizes[name], len(content))
		}
	}
	if sizes["proj"] != diskUsage(t, filepath.Join(h.root, "proj"), true) {
		t.Errorf("du -ab proj = %d, want the byte sum", sizes["proj"])
	}
	if sizes["proj/src"] != sizes["proj/src/main.go"]+sizes["proj/src/lib/util.go"] || sizes["proj/empty"] != 0 {
		t.Errorf("du -ab directory sizes %v, want their files' bytes", sizes)
	}
}

// TestDuCountsHardLinksOnceAndNeverFollowsLinks: du charges a file's
// blocks to its first link only and sizes a symbolic link itself.
func TestDuCountsHardLinksOnceAndNeverFollowsLinks(t *testing.T) {
	h := newHarness(t, map[string]string{"d/big": strings.Repeat("x", 5000)})
	testutil.FailErr(t, "hard link", os.Link(filepath.Join(h.root, "d", "big"), filepath.Join(h.root, "d", "same")))
	symlink(t, "big", filepath.Join(h.root, "d", "zlink"))
	lines := duLines(t, h, "du", "-ab", "d")
	if got := paths(lines); got != "d/big d/zlink d" {
		t.Fatalf("du -ab d printed %q", got)
	}
	if lines[1][0] != "3" {
		t.Fatalf("link sized %s, want its own 3 bytes", lines[1][0])
	}
	if want := strconv.FormatInt(5000+3, 10); lines[2][0] != want {
		t.Fatalf("du -ab d = %s, want %s with big counted once", lines[2][0], want)
	}
}

func TestDuFailuresAndRefusals(t *testing.T) {
	h := newHarness(t, duFiles)
	h.exec("", "du", "-b", "missing", "proj/a.txt").expect(t, "du missing", "6\tproj/a.txt\n", "du: cannot access 'missing': No such file or directory\n", 1)
	for _, argv := range []string{"du -sa proj", "du -s -d 1 proj", "du -d x proj", "du -d -1 proj"} {
		h.exec("", strings.Fields(argv)...).refused(t, argv, "COMMAND_OPERAND_INVALID")
	}
	h.exec("", "du", "-sd", "0", "proj").expect(t, "du -s -d 0", h.exec("", "du", "-s", "proj").stdout, "", 0)
}

func TestDuUnreadableDirectoryIsReportedAndCounted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind the superuser")
	}
	h := newHarness(t, map[string]string{"d/closed/inner": "x\n", "d/open": "y\n"})
	closed := filepath.Join(h.root, "d", "closed")
	testutil.FailErr(t, "close dir", os.Chmod(closed, 0))
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
	r := h.exec("", "du", "-b", "d")
	if r.result.Status != 1 || !strings.Contains(r.stderr, "du: cannot read directory '") || !strings.HasSuffix(r.stdout, "\td\n") {
		t.Fatalf("du over unreadable directory: %+v stdout %q stderr %q", r.result, r.stdout, r.stderr)
	}
}

// TestDuRefusesAWalkOverTheVisitBudget: a capped walk stops the program
// with the typed limit, never a short total.
func TestDuRefusesAWalkOverTheVisitBudget(t *testing.T) {
	h := newHarness(t, duFiles)
	h.owners.overrides = map[string]int64{"traversal_visits": 3}
	r := h.exec("", "du", "-s", "proj")
	r.refused(t, "du over budget", "COMMAND_LIMIT_EXCEEDED")
	if r.stdout != "" {
		t.Fatalf("capped du printed %q", r.stdout)
	}
	h.owners.overrides = map[string]int64{"traversal_visits": 8}
	h.exec("", "du", "-s", "proj").expect(t, "du at budget", h.exec("", "du", "-s", "proj").stdout, "", 0)
}
