package mutation

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func modTime(t *testing.T, h *harness, rel string) time.Time {
	t.Helper()
	info, err := os.Stat(h.path(rel))
	testutil.FailErr(t, "stat "+rel, err)
	return info.ModTime()
}

func accessTime(t *testing.T, h *harness, rel string) time.Time {
	t.Helper()
	info, err := os.Stat(h.path(rel))
	testutil.FailErr(t, "stat "+rel, err)
	accessed, ok := fseffect.AccessTime(info)
	if !ok {
		t.Skip("platform reports no access time")
	}
	return accessed
}

func TestTouchExplicitTimes(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		args []string
		want time.Time
	}{
		{"-t MMDDhhmm uses this year", []string{"-t", "10201200"}, time.Date(now.UTC().Year(), 10, 20, 12, 0, 0, 0, time.UTC)},
		{"-t YYMMDDhhmm.ss", []string{"-t", "2610201200.30"}, time.Date(2026, 10, 20, 12, 0, 30, 0, time.UTC)},
		{"-t 69 is the twentieth century", []string{"-t", "6901020304"}, time.Date(1969, 1, 2, 3, 4, 0, 0, time.UTC)},
		{"-t CCYYMMDDhhmm", []string{"-t", "203001020304"}, time.Date(2030, 1, 2, 3, 4, 0, 0, time.UTC)},
		{"-d ISO date", []string{"-d", "2026-10-20"}, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)},
		{"-d ISO date and time", []string{"-d", "2026-10-20 12:00:30"}, time.Date(2026, 10, 20, 12, 0, 30, 0, time.UTC)},
		{"-d RFC 3339", []string{"--date", "2026-10-20T12:00:30Z"}, time.Date(2026, 10, 20, 12, 0, 30, 0, time.UTC)},
		{"-d offset", []string{"-d", "2026-10-20T12:00:30+02:00"}, time.Date(2026, 10, 20, 10, 0, 30, 0, time.UTC)},
		{"-d epoch", []string{"-d", "@1792560000"}, time.Unix(1792560000, 0)},
		{"-d RFC 1123", []string{"-d", "Tue, 20 Oct 2026 12:00:30 GMT"}, time.Date(2026, 10, 20, 12, 0, 30, 0, time.UTC)},
		{"-d relative to a date", []string{"-d", "2026-10-20 +1 day"}, time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)},
		{"-d month name", []string{"-d", "Oct 20 2026 3pm"}, time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{"old.txt": "kept"})
			for _, target := range []string{"old.txt", "new.txt"} {
				r := h.run("", append(append([]string{"touch"}, c.args...), target)...)
				if r.err != nil || r.result.Status != 0 {
					t.Fatalf("touch %s: status %d %v", target, r.result.Status, r.err)
				}
				if got := modTime(t, h, target); !got.Equal(c.want) {
					t.Fatalf("%s mtime %v, want %v", target, got, c.want)
				}
			}
			if h.read("old.txt") != "kept" || h.read("new.txt") != "" {
				t.Fatal("touch changed file bytes")
			}
		})
	}
}

func TestTouchReadsTimesInTheProgramTimeZone(t *testing.T) {
	h := newHarness(t, map[string]string{"a.txt": "a"})
	h.env = []string{"TZ=America/New_York"}
	for _, args := range [][]string{{"-t", "202610201200"}, {"-d", "2026-10-20 12:00"}} {
		if r := h.run("", append(append([]string{"touch"}, args...), "a.txt")...); r.err != nil || r.result.Status != 0 {
			t.Fatalf("touch %v: %+v %v", args, r.result, r.err)
		}
		if got := modTime(t, h, "a.txt"); !got.Equal(time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC)) {
			t.Fatalf("touch %v set %v", args, got.UTC())
		}
	}
}

func TestTouchRelativeDatesAndReference(t *testing.T) {
	h := newHarness(t, map[string]string{"ref.txt": "r", "a.txt": "a", "m.txt": "m"})
	stamp := time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)
	testutil.FailErr(t, "stamp reference", os.Chtimes(h.path("ref.txt"), stamp.Add(time.Hour), stamp))
	if r := h.run("", "touch", "-r", "ref.txt", "a.txt"); r.err != nil {
		t.Fatalf("touch -r: %v", r.err)
	}
	if got := modTime(t, h, "a.txt"); !got.Equal(stamp) {
		t.Fatalf("reference mtime %v", got)
	}
	if got := accessTime(t, h, "a.txt"); !got.Equal(stamp.Add(time.Hour)) {
		t.Fatalf("reference atime %v", got)
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	testutil.FailErr(t, "age m.txt", os.Chtimes(h.path("m.txt"), old, old))
	if r := h.run("", "touch", "-m", "-d", "2 days ago", "m.txt"); r.err != nil {
		t.Fatalf("touch -m -d: %v", r.err)
	}
	if got := time.Since(modTime(t, h, "m.txt")); got < 47*time.Hour || got > 49*time.Hour {
		t.Fatalf("relative mtime is %v ago", got)
	}
	// Reading the file for its revision may refresh the access time; -m must
	// not set it to the requested time.
	if got := time.Since(accessTime(t, h, "m.txt")); got > 47*time.Hour && got < 49*time.Hour {
		t.Fatal("-m set the access time")
	}
	if r := h.run("", "touch", "-a", "-t", "202001020304", "m.txt"); r.err != nil || time.Since(modTime(t, h, "m.txt")) < 47*time.Hour {
		t.Fatalf("touch -a -t changed the modification time: %v", r.err)
	}
	if r := h.run("", "touch", "-d", "yesterday", "m.txt"); r.err != nil || time.Since(modTime(t, h, "m.txt")) < 23*time.Hour {
		t.Fatalf("touch -d yesterday: %v", r.err)
	}
	for _, args := range [][]string{{"-t", "10201200", "-d", "now"}, {"-r", "missing.txt"}} {
		r := h.run("", append(append([]string{"touch"}, args...), "a.txt")...)
		if rejected := toolrejection.AsToolReject(r.err); rejected == nil || rejected.Code != "COMMAND_OPERAND_INVALID" {
			t.Fatalf("touch %v accepted: %v", args, r.err)
		}
	}
	for _, args := range [][]string{{"-t", "2026"}, {"-t", "13011200"}, {"-d", "next blue moon"}} {
		r := h.run("", append(append([]string{"touch"}, args...), "a.txt")...)
		if r.err != nil || r.result.Status != 1 || r.stderr != "touch: invalid date format '"+args[1]+"'\n" {
			t.Fatalf("touch %v: status %d stderr %q %v", args, r.result.Status, r.stderr, r.err)
		}
	}
	if got := modTime(t, h, "a.txt"); !got.Equal(stamp) {
		t.Fatal("refused touch changed the file")
	}
}

func gunzipString(t *testing.T, data []byte) string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	testutil.FailErr(t, "open gzip", err)
	out, err := io.ReadAll(gz)
	testutil.FailErr(t, "read gzip", err)
	return string(out)
}

func TestGzipStreamKeepForceAndDecompressForms(t *testing.T) {
	h := newHarness(t, map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n", "b.txt.gz": "stale"})
	r := h.run("", "gzip", "-c", "a.txt")
	if r.err != nil || gunzipString(t, []byte(r.stdout)) != "alpha\n" || h.read("a.txt") != "alpha\n" || !h.absent("a.txt.gz") {
		t.Fatalf("gzip -c: %v", r.err)
	}
	if r := h.run("", "gzip", "-k", "a.txt"); r.err != nil || h.read("a.txt") != "alpha\n" || gunzipString(t, []byte(h.read("a.txt.gz"))) != "alpha\n" {
		t.Fatalf("gzip -k: %v", r.err)
	}
	if r := h.run("", "gzip", "b.txt"); r.result.Status != 1 || h.read("b.txt.gz") != "stale" || h.read("b.txt") != "beta\n" {
		t.Fatalf("gzip replaced an existing output: %+v", r.result)
	}
	if r := h.run("", "gzip", "-f", "b.txt"); r.err != nil || gunzipString(t, []byte(h.read("b.txt.gz"))) != "beta\n" || !h.absent("b.txt") {
		t.Fatalf("gzip -f: %v", r.err)
	}
	if r := h.run("", "gzip", "-d", "-k", "b.txt.gz"); r.err != nil || h.read("b.txt") != "beta\n" || h.absent("b.txt.gz") {
		t.Fatalf("gzip -dk: %v", r.err)
	}
	if r := h.run("", "gunzip", "-c", "a.txt.gz"); r.err != nil || r.stdout != "alpha\n" || h.absent("a.txt.gz") {
		t.Fatalf("gunzip -c: %v", r.err)
	}
	testutil.FailErr(t, "seed tgz", os.WriteFile(h.path("bundle.tgz"), gzipBytes(t, []byte("tarball")), 0o644))
	if r := h.run("", "gunzip", "bundle.tgz"); r.err != nil || h.read("bundle.tar") != "tarball" || !h.absent("bundle.tgz") {
		t.Fatalf("gunzip .tgz: %v", r.err)
	}
	testutil.FailErr(t, "seed suffixless", os.WriteFile(h.path("payload"), gzipBytes(t, []byte("raw")), 0o644))
	if r := h.run("", "gunzip", "-c", "payload"); r.err != nil || r.stdout != "raw" {
		t.Fatalf("gunzip -c without suffix: %q %v", r.stdout, r.err)
	}
	filtered := h.run("streamed\n", "gzip")
	if filtered.err != nil || gunzipString(t, []byte(filtered.stdout)) != "streamed\n" {
		t.Fatalf("gzip filter: %v", filtered.err)
	}
	if r := h.run(filtered.stdout, "gunzip"); r.err != nil || r.stdout != "streamed\n" {
		t.Fatalf("gunzip filter: %q %v", r.stdout, r.err)
	}
	if r := h.run(filtered.stdout, "gzip", "-d"); r.err != nil || r.stdout != "streamed\n" {
		t.Fatalf("gzip -d filter: %q %v", r.stdout, r.err)
	}
}

func TestGzipListMatchesGNUColumns(t *testing.T) {
	h := newHarness(t, map[string]string{"a.txt": strings.Repeat("alpha\n", 100), "b.txt": "b"})
	for _, name := range []string{"a.txt", "b.txt"} {
		if r := h.run("", "gzip", "-k", name); r.err != nil {
			t.Fatalf("gzip %s: %v", name, r.err)
		}
	}
	row := func(name string, raw int64) (int64, string) {
		compressed := int64(len(h.read(name + ".gz")))
		header := int64(10 + len(name) + 1 + 8)
		return compressed, fmt.Sprintf("%19d %19d %s %s\n", compressed, raw, gzipRatio(raw-(compressed-header), raw), name)
	}
	aIn, aRow := row("a.txt", 600)
	bIn, bRow := row("b.txt", 1)
	header := fmt.Sprintf("%19s %19s  ratio uncompressed_name\n", "compressed", "uncompressed")
	totals := fmt.Sprintf("%19d %19d %s (totals)\n", aIn+bIn, 601, gzipRatio(601-(aIn+bIn-int64(10+6+8)*2), 601))
	for _, command := range []string{"gzip", "gunzip"} {
		r := h.run("", command, "-l", "a.txt.gz", "b.txt.gz")
		if r.err != nil || r.stdout != header+aRow+bRow+totals {
			t.Fatalf("%s -l:\n%s\nwant:\n%s", command, r.stdout, header+aRow+bRow+totals)
		}
	}
	if !strings.Contains(aRow, " 9") || !strings.Contains(bRow, "-") {
		t.Fatalf("ratios do not reflect saving and growth: %q %q", aRow, bRow)
	}
	if got := gzipRatio(-6, 3); got != "-200.0%" {
		t.Fatalf("ratio %q", got)
	}
	if got := gzipRatio(1, 3); got != " 33.3%" {
		t.Fatalf("ratio %q", got)
	}
}

func TestVerboseMutationMessages(t *testing.T) {
	h := newHarness(t, map[string]string{"a.txt": "a", "it's.txt": "q", "dir/inner.txt": "i", "gone.txt": "g", "tree/x.txt": "x", "tree/sub/": ""})
	cases := []struct {
		args   []string
		stdout string
	}{
		{[]string{"cp", "-v", "a.txt", "b.txt"}, "'a.txt' -> 'b.txt'\n"},
		{[]string{"cp", "-v", "it's.txt", "dir"}, `"it's.txt" -> "dir/it's.txt"` + "\n"},
		{[]string{"mv", "-v", "b.txt", "c.txt"}, "renamed 'b.txt' -> 'c.txt'\n"},
		{[]string{"mv", "--verbose", "c.txt", "dir/"}, "renamed 'c.txt' -> 'dir/c.txt'\n"},
		{[]string{"mkdir", "-v", "made"}, "mkdir: created directory 'made'\n"},
		{[]string{"rm", "-v", "gone.txt"}, "removed 'gone.txt'\n"},
	}
	for _, c := range cases {
		r := h.run("", c.args...)
		if r.err != nil || r.result.Status != 0 || r.stdout != c.stdout {
			t.Fatalf("%v: stdout %q status %d err %v", c.args, r.stdout, r.result.Status, r.err)
		}
	}
	tree := h.run("", "rm", "-rv", "tree")
	lines := strings.Split(strings.TrimSuffix(tree.stdout, "\n"), "\n")
	slices.Sort(lines)
	if tree.err != nil || !slices.Equal(lines, []string{"removed 'tree/x.txt'", "removed directory 'tree'", "removed directory 'tree/sub'"}) || !strings.HasSuffix(tree.stdout, "removed directory 'tree'\n") {
		t.Fatalf("rm -rv: %q %v", tree.stdout, tree.err)
	}
	if !h.absent("tree") || h.read("dir/c.txt") != "a" || h.read("dir/it's.txt") != "q" {
		t.Fatal("verbose mutations did not apply")
	}
	pv := h.run("", "mkdir", "-pv", "deep/er/est")
	if pv.err != nil || pv.stdout != "mkdir: created directory 'deep'\nmkdir: created directory 'deep/er'\nmkdir: created directory 'deep/er/est'\n" {
		t.Fatalf("mkdir -pv: %q %v", pv.stdout, pv.err)
	}
	if r := h.run("", "cp", "a.txt", "quiet.txt"); r.stdout != "" {
		t.Fatalf("cp without -v printed %q", r.stdout)
	}
}

func TestCpArchiveCopiesTreeAndRefusesLinks(t *testing.T) {
	h := newHarness(t, map[string]string{"src/run.sh": "#!/bin/sh\n", "src/sub/data.txt": "data"})
	testutil.FailErr(t, "mode", os.Chmod(h.path("src/run.sh"), 0o750))
	stamp := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	testutil.FailErr(t, "stamp", os.Chtimes(h.path("src/sub/data.txt"), stamp, stamp))
	if r := h.run("", "cp", "-a", "src", "copy"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("cp -a: %d %v", r.result.Status, r.err)
	}
	info, err := os.Stat(h.path("copy/run.sh"))
	testutil.FailErr(t, "stat copy", err)
	if info.Mode().Perm() != 0o750 || h.read("copy/sub/data.txt") != "data" || !modTime(t, h, "copy/sub/data.txt").Equal(stamp) {
		t.Fatalf("cp -a lost mode %v or time %v", info.Mode(), modTime(t, h, "copy/sub/data.txt"))
	}
	testutil.FailErr(t, "link", os.Symlink("run.sh", h.path("src/link")))
	r := h.run("", "cp", "-a", "src", "linked")
	if rejected := toolrejection.AsToolReject(r.err); rejected == nil || rejected.Code != "COMMAND_NATIVE_FORM_UNSUPPORTED" || !h.absent("linked") {
		t.Fatalf("cp -a copied a link tree: %v", r.err)
	}
}

func TestTarVerboseListingAndMemberSelection(t *testing.T) {
	h := newHarness(t, map[string]string{"proj/a.txt": "alpha", "proj/sub/b.txt": "be"})
	create := h.run("", "tar", "-cvf", "proj.tar", "proj")
	if create.err != nil || create.stdout != "proj/\nproj/a.txt\nproj/sub/\nproj/sub/b.txt\n" {
		t.Fatalf("tar -cv: %q %v", create.stdout, create.err)
	}
	stamp := time.Date(2026, 10, 20, 12, 30, 0, 0, time.UTC)
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for _, hdr := range []*tar.Header{
		{Name: "top/", Typeflag: tar.TypeDir, Mode: 0o755, Uname: "dev", Gname: "staff", ModTime: stamp},
		{Name: "top/run.sh", Typeflag: tar.TypeReg, Mode: 0o4755, Size: 3, Uname: "dev", Gname: "staff", ModTime: stamp},
		{Name: "top/link", Typeflag: tar.TypeSymlink, Linkname: "run.sh", Mode: 0o777, Uid: 501, Gid: 20, ModTime: stamp},
		{Name: "other.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2, Uname: "a-very-long-owner-name", Gname: "group", ModTime: stamp},
	} {
		testutil.FailErr(t, "header", tw.WriteHeader(hdr))
		_, err := tw.Write(make([]byte, hdr.Size))
		testutil.FailErr(t, "body", err)
	}
	testutil.FailErr(t, "close tar", tw.Close())
	testutil.FailErr(t, "seed tar", os.WriteFile(h.path("listing.tar"), archive.Bytes(), 0o644))
	when := stamp.Local().Format("2006-01-02 15:04")
	// GNU pads owner/group and size to a column of 19 that grows with the
	// widest entry seen so far.
	want := "drwxr-xr-x dev/staff" + strings.Repeat(" ", 9) + "0 " + when + " top/\n" +
		"-rwsr-xr-x dev/staff" + strings.Repeat(" ", 9) + "3 " + when + " top/run.sh\n" +
		"lrwxrwxrwx 501/20" + strings.Repeat(" ", 12) + "0 " + when + " top/link -> run.sh\n" +
		"-rw-r--r-- a-very-long-owner-name/group 2 " + when + " other.txt\n"
	if r := h.run("", "tar", "-tvf", "listing.tar"); r.err != nil || r.stdout != want {
		t.Fatalf("tar -tv:\n%s\nwant:\n%s", r.stdout, want)
	}
	if r := h.run("", "tar", "-tf", "proj.tar", "proj/sub"); r.err != nil || r.stdout != "proj/sub/\nproj/sub/b.txt\n" {
		t.Fatalf("tar -t member: %q %v", r.stdout, r.err)
	}
	testutil.FailErr(t, "out", os.Mkdir(h.path("out"), 0o755))
	extract := h.run("", "tar", "-xvf", "proj.tar", "-C", "out", "--strip-components", "1", "proj/sub")
	if extract.err != nil || extract.stdout != "proj/sub/\nproj/sub/b.txt\n" || h.read("out/sub/b.txt") != "be" || !h.absent("out/a.txt") {
		t.Fatalf("tar -xv member: %q %v", extract.stdout, extract.err)
	}
}

// BSD and GNU share these spellings; each is accepted with the shared meaning.
func TestSharedBSDSpellings(t *testing.T) {
	h := newHarness(t, map[string]string{"a.txt": strings.Repeat("alpha\n", 200), "empty/": "", "full/x.txt": "x", "c.txt": "c"})
	if r := h.run("", "touch", "-f", "a.txt"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("touch -f: %v", r.err)
	}
	if r := h.run("", "cp", "-c", "-X", "c.txt", "c2.txt"); r.err != nil || h.read("c2.txt") != "c" {
		t.Fatalf("cp -c -X: %v", r.err)
	}
	if r := h.run("", "rm", "-d", "empty"); r.err != nil || !h.absent("empty") {
		t.Fatalf("rm -d empty: %v", r.err)
	}
	if r := h.run("", "rm", "-d", "full"); h.absent("full/x.txt") || (r.err == nil && r.result.Status == 0) {
		t.Fatalf("rm -d removed a non-empty directory: %+v %v", r.result, r.err)
	}
	fast, best := h.run("", "gzip", "-c", "-1", "a.txt"), h.run("", "gzip", "-c", "--best", "a.txt")
	if fast.err != nil || best.err != nil || gunzipString(t, []byte(fast.stdout)) != h.read("a.txt") || gunzipString(t, []byte(best.stdout)) != h.read("a.txt") {
		t.Fatalf("gzip levels: %v %v", fast.err, best.err)
	}
	noName := h.run("", "gzip", "-c", "-n", "a.txt")
	reader, err := gzip.NewReader(bytes.NewReader([]byte(noName.stdout)))
	testutil.FailErr(t, "read -n member", err)
	if reader.Name != "" || !reader.ModTime.IsZero() {
		t.Fatalf("gzip -n stored %q %v", reader.Name, reader.ModTime)
	}
	if r := h.run("", "gzip", "-k", "-q", "a.txt"); r.err != nil || h.absent("a.txt.gz") {
		t.Fatalf("gzip -q: %v", r.err)
	}
	if r := h.run("", "gzip", "-t", "a.txt.gz"); r.err != nil || r.result.Status != 0 || r.stdout != "" || !h.absent("a.txt.gz.out") {
		t.Fatalf("gzip -t: %+v %v", r.result, r.err)
	}
	testutil.FailErr(t, "seed corrupt", os.WriteFile(h.path("bad.gz"), []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 3, 1, 2, 3}, 0o644))
	if r := h.run("", "gunzip", "-t", "bad.gz"); r.result.Status != 1 || h.absent("bad.gz") {
		t.Fatalf("gunzip -t corrupt: %+v %v", r.result, r.err)
	}
}

func lookGNU(name string) (string, bool) {
	for _, candidate := range []string{"/opt/homebrew/bin/" + name, "/usr/local/bin/" + name} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	path, err := exec.LookPath(name)
	return path, err == nil
}

func runExternal(t *testing.T, dir, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	testutil.FailErr(t, bin+" "+strings.Join(args, " "), err)
	return string(out)
}

func TestTarGzipAndMutationsMatchGNU(t *testing.T) {
	gtar, ok := lookGNU("gtar")
	if !ok {
		t.Skip("no gtar on this host")
	}
	h := newHarness(t, map[string]string{"proj/a.txt": "alpha\n", "proj/sub/b.txt": "beta\n"})
	if r := h.run("", "tar", "-cf", "native.tar", "proj"); r.err != nil {
		t.Fatalf("tar -cf: %v", r.err)
	}
	stamp := time.Date(2026, 10, 20, 12, 30, 0, 0, time.UTC)
	var listing bytes.Buffer
	tw := tar.NewWriter(&listing)
	for _, hdr := range []*tar.Header{
		{Name: "top/", Typeflag: tar.TypeDir, Mode: 0o755, Uname: "dev", Gname: "staff", ModTime: stamp},
		{Name: "top/run.sh", Typeflag: tar.TypeReg, Mode: 0o4755, Size: 3, Uname: "dev", Gname: "staff", ModTime: stamp},
		{Name: "top/link", Typeflag: tar.TypeSymlink, Linkname: "run.sh", Mode: 0o777, Uid: 501, Gid: 20, ModTime: stamp},
		{Name: "other.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2, Uname: "a-very-long-owner-name", Gname: "group", ModTime: stamp},
	} {
		testutil.FailErr(t, "header", tw.WriteHeader(hdr))
		_, err := tw.Write(make([]byte, hdr.Size))
		testutil.FailErr(t, "body", err)
	}
	testutil.FailErr(t, "close tar", tw.Close())
	testutil.FailErr(t, "write listing.tar", os.WriteFile(h.path("listing.tar"), listing.Bytes(), 0o644))

	for _, args := range [][]string{
		{"-tf", "native.tar"},
		{"-tf", "native.tar", "proj/sub"},
		{"-tvf", "listing.tar"},
	} {
		want := runExternal(t, h.root, gtar, args...)
		got := h.run("", append([]string{"tar"}, args...)...)
		if got.err != nil || got.stdout != want {
			t.Fatalf("tar %v:\n got %q\nwant %q (%v)", args, got.stdout, want, got.err)
		}
	}
	testutil.FailErr(t, "mkdir out-native", os.Mkdir(h.path("out-native"), 0o755))
	testutil.FailErr(t, "mkdir out-gnu", os.Mkdir(h.path("out-gnu"), 0o755))
	wantExtract := runExternal(t, h.root, gtar, "-xvf", "native.tar", "-C", "out-gnu", "--strip-components", "1", "proj/sub")
	gotExtract := h.run("", "tar", "-xvf", "native.tar", "-C", "out-native", "--strip-components", "1", "proj/sub")
	if gotExtract.err != nil || gotExtract.stdout != wantExtract || h.read("out-native/sub/b.txt") != h.read("out-gnu/sub/b.txt") {
		t.Fatalf("tar -xvf strip:\n got %q\nwant %q (%v)", gotExtract.stdout, wantExtract, gotExtract.err)
	}
	// Archive created by gtar -czf is listed and extracted identically by native tar.
	runExternal(t, h.root, gtar, "-czf", "gnu.tgz", "proj")
	if r := h.run("", "tar", "-tf", "gnu.tgz"); r.err != nil || r.stdout != runExternal(t, h.root, gtar, "-tf", "gnu.tgz") {
		t.Fatalf("tar -tf gnu.tgz: %q %v", r.stdout, r.err)
	}
	testutil.FailErr(t, "mkdir from-gnu", os.Mkdir(h.path("from-gnu"), 0o755))
	if r := h.run("", "tar", "-xzf", "gnu.tgz", "-C", "from-gnu"); r.err != nil || h.read("from-gnu/proj/a.txt") != "alpha\n" {
		t.Fatalf("tar -xzf gnu.tgz: %v", r.err)
	}
	if sysGzip, err := exec.LookPath("/usr/bin/gzip"); err == nil {
		sysCompressed := runExternal(t, h.root, sysGzip, "-c", "-n", "proj/a.txt")
		if r := h.run(sysCompressed, "zcat"); r.err != nil || r.stdout != "alpha\n" {
			t.Fatalf("zcat on system gzip output: %q %v", r.stdout, r.err)
		}
	}
	if gmkdir, ok := lookGNU("gmkdir"); ok {
		gnuDir := t.TempDir()
		want := strings.ReplaceAll(runExternal(t, gnuDir, gmkdir, "-pv", "d/e/f"), gmkdir, "mkdir")
		got := h.run("", "mkdir", "-pv", "d/e/f")
		if got.err != nil || got.stdout != want {
			t.Fatalf("mkdir -pv:\n got %q\nwant %q", got.stdout, want)
		}
	}
	if gcp, ok := lookGNU("gcp"); ok {
		gnuDir := t.TempDir()
		testutil.FailErr(t, "seed gnu cp", os.WriteFile(filepath.Join(gnuDir, "a.txt"), []byte("a"), 0o644))
		want := runExternal(t, gnuDir, gcp, "-v", "a.txt", "b.txt")
		got := h.run("", "cp", "-v", "proj/a.txt", "b.txt")
		if got.err != nil || strings.ReplaceAll(got.stdout, "proj/a.txt", "a.txt") != want {
			t.Fatalf("cp -v:\n got %q\nwant %q", got.stdout, want)
		}
	}
}
