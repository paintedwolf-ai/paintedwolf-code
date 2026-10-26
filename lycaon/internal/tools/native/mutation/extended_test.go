package mutation

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	_, err := w.Write(data)
	testutil.FailErr(t, "gzip", err)
	testutil.FailErr(t, "gzip close", w.Close())
	return out.Bytes()
}

// pngHeader is a real PNG signature and IHDR chunk: non-UTF-8 binary.
var pngHeader = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89}

func TestRedirectPublishesExactBinaryAndCRLFBytes(t *testing.T) {
	compressed := gzipBytes(t, []byte(strings.Repeat("payload\n", 100)))
	cases := []struct {
		name, operator string
		seed, write    []byte
		want           []byte
	}{
		{"binary into absent", ">", nil, pngHeader, pngHeader},
		{"gzip stream over binary", ">", pngHeader, compressed, compressed},
		{"binary append", ">>", pngHeader, []byte{0xff, 0xfe, 0x00}, append(slices.Clone(pngHeader), 0xff, 0xfe, 0x00)},
		{"high byte only", ">", nil, []byte{0xff}, []byte{0xff}},
		{"CRLF text stays CRLF", ">", nil, []byte("a\r\nb\r\n"), []byte("a\r\nb\r\n")},
		{"text append onto CRLF file", ">>", []byte("one\r\n"), []byte("two\n"), []byte("one\r\ntwo\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			if c.seed != nil {
				testutil.FailErr(t, "seed", os.WriteFile(h.path("out.bin"), c.seed, 0o644))
			}
			var reviewed []tools.FileChange
			h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
				reviewed = append(reviewed, changes...)
				return nil
			}
			staged, err := h.publication().Open(t.Context(), h.destination(c.operator, "out.bin"))
			testutil.FailErr(t, "open", err)
			_, err = staged.Write(c.write)
			testutil.FailErr(t, "stage", err)
			_, err = staged.Publish(t.Context())
			testutil.FailErr(t, "publish", err)
			got, err := os.ReadFile(h.path("out.bin"))
			testutil.FailErr(t, "read", err)
			if !bytes.Equal(got, c.want) {
				t.Fatalf("published % x, want % x", got, c.want)
			}
			if len(reviewed) == 0 {
				t.Fatal("binary publication skipped review")
			}
			preview := reviewed[len(reviewed)-1].Preview
			if preview.AfterBytes != int64(len(c.want)) || preview.AfterSHA256 == "" {
				t.Fatalf("review lacks the size and hash summary: %+v", preview)
			}
			if !utf8.Valid(c.want) && (preview.After != "" || preview.PreviewNote == "") {
				t.Fatalf("binary review exposed raw bytes: after=%q note=%q", preview.After, preview.PreviewNote)
			}
		})
	}
}

func TestBinaryDestinationConflictsWhenItMoves(t *testing.T) {
	h := newHarness(t, nil)
	testutil.FailErr(t, "seed", os.WriteFile(h.path("img.png"), pngHeader, 0o644))
	staged, err := h.publication().Open(t.Context(), h.destination(">", "img.png"))
	testutil.FailErr(t, "open", err)
	_, _ = staged.Write([]byte{0xff, 0xd8})
	changed := append(slices.Clone(pngHeader), 0)
	testutil.FailErr(t, "concurrent change", os.WriteFile(h.path("img.png"), changed, 0o644))
	if _, err := staged.Publish(t.Context()); !conflictCode(err) {
		t.Fatalf("moved binary destination did not conflict: %v", err)
	}
	if got, _ := os.ReadFile(h.path("img.png")); !bytes.Equal(got, changed) {
		t.Fatal("conflicting binary publish replaced the change")
	}
}

func TestTouchAndCopyPreserveTimestamps(t *testing.T) {
	h := newHarness(t, map[string]string{"old.txt": "old\n", "src.txt": "source\n"})
	past := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	testutil.FailErr(t, "age files", os.Chtimes(h.path("old.txt"), past, past))
	testutil.FailErr(t, "age source", os.Chtimes(h.path("src.txt"), past, past))
	testutil.FailErr(t, "source mode", os.Chmod(h.path("src.txt"), 0o640))
	if r := h.run("", "touch", "-a", "old.txt"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("touch -a %+v %v", r.result, r.err)
	}
	if info, _ := os.Stat(h.path("old.txt")); !info.ModTime().Equal(past) {
		t.Fatalf("touch -a changed the modification time: %v", info.ModTime())
	}
	if r := h.run("", "touch", "old.txt"); r.err != nil || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("touch existing %+v %v", r.result, r.err)
	}
	info, _ := os.Stat(h.path("old.txt"))
	if !info.ModTime().After(past) || h.read("old.txt") != "old\n" {
		t.Fatalf("touch did not update the time or changed content: %v", info.ModTime())
	}
	if r := h.run("", "cp", "-p", "src.txt", "dst.txt"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("cp -p %+v %v", r.result, r.err)
	}
	dst, _ := os.Stat(h.path("dst.txt"))
	if !dst.ModTime().Equal(past) || dst.Mode().Perm() != 0o640 || h.read("dst.txt") != "source\n" {
		t.Fatalf("cp -p preserved mtime=%v mode=%v", dst.ModTime(), dst.Mode())
	}
}

func TestRecursiveCopyPreservesDirectoryMetadata(t *testing.T) {
	h := newHarness(t, map[string]string{"src/nested/item.txt": "content\n"})
	past := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	testutil.FailErr(t, "source directory mode", os.Chmod(h.path("src/nested"), 0o700))
	testutil.FailErr(t, "source directory time", os.Chtimes(h.path("src/nested"), past, past))
	if r := h.run("", "cp", "-r", "-p", "src", "dst"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("cp -r -p: %+v %v %q", r.result, r.err, r.stderr)
	}
	info, err := os.Stat(h.path("dst/nested"))
	testutil.FailErr(t, "copied directory", err)
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !info.ModTime().Equal(past) {
		t.Fatalf("copied directory mode=%v mtime=%v", info.Mode(), info.ModTime())
	}
	if got := h.read("dst/nested/item.txt"); got != "content\n" {
		t.Fatalf("copied content = %q", got)
	}
}

// A directory move is one journaled rename of the entry itself; moving it
// back restores the tree exactly.
func TestDirectoryMoveIsJournaledAndReversible(t *testing.T) {
	h := newHarness(t, map[string]string{"pkg/a.go": "package a\n", "pkg/sub/b.txt": "b\n", "dest/": ""})
	if r := h.run("", "mv", "pkg", "renamed"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("mv dir %+v %v %q", r.result, r.err, r.stderr)
	}
	if !h.absent("pkg") || h.read("renamed/a.go") != "package a\n" || h.read("renamed/sub/b.txt") != "b\n" {
		t.Fatal("directory was not renamed whole")
	}
	var journaled int
	testutil.FailErr(t, "read journal", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_mutations WHERE plan_json LIKE '%renamed%' AND status NOT IN ('prepared')`).Scan(&journaled))
	if journaled == 0 {
		t.Fatal("directory rename left no settled source journal entry")
	}
	if r := h.run("", "mv", "renamed", "dest"); r.err != nil || h.read("dest/renamed/sub/b.txt") != "b\n" {
		t.Fatalf("mv dir into existing dir %+v %v", r.result, r.err)
	}
	if r := h.run("", "mv", "dest/renamed", "pkg"); r.err != nil || h.read("pkg/a.go") != "package a\n" || h.read("pkg/sub/b.txt") != "b\n" || !h.absent("dest/renamed") {
		t.Fatalf("moving the directory back did not restore it: %+v %v", r.result, r.err)
	}
	// Replacing an existing directory with mv sets the replaced directory aside first so Undo restores it.
	testutil.FailErr(t, "mkdir replaced dir", os.MkdirAll(h.path("dest/pkg"), 0o755))
	testutil.FailErr(t, "seed replaced dir file", os.WriteFile(h.path("dest/pkg/old.txt"), []byte("old-dir-content\n"), 0o644))
	if r := h.run("", "mv", "pkg", "dest"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("mv dir onto existing dir: %+v %v %q", r.result, r.err, r.stderr)
	}
	if !h.absent("pkg") || h.read("dest/pkg/a.go") != "package a\n" || !h.absent("dest/pkg/old.txt") {
		t.Fatal("mv did not replace destination directory with source directory")
	}
	if r := h.run("", "mv", "dest/pkg", "pkg"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("move replacement out of the way before Undo: %+v %v", r.result, r.err)
	}
	h.undoAll(1)
	if h.read("dest/pkg/old.txt") != "old-dir-content\n" {
		t.Fatal("Undo did not restore the replaced directory")
	}
}

func TestChownNumericForms(t *testing.T) {
	h := newHarness(t, map[string]string{"f.txt": "f\n"})
	uid, gid := os.Getuid(), os.Getgid()
	for _, spec := range []string{fmt.Sprint(uid), fmt.Sprintf("%d:%d", uid, gid)} {
		if r := h.run("", "chown", spec, "f.txt"); r.err != nil || r.result.Status != 0 {
			t.Fatalf("chown %s: %+v %v", spec, r.result, r.err)
		}
	}
	for _, spec := range []string{"root", "1:wheel", ":1"} {
		if r := h.run("", "chown", spec, "f.txt"); r.result.Terminal != toolcontract.ProducerRefused {
			t.Fatalf("chown %s accepted a non-numeric form: %+v", spec, r.result)
		}
	}
	if r := h.run("", "chown", fmt.Sprint(uid+12345), "f.txt"); r.result.Terminal != toolcontract.ProducerRefused {
		t.Fatalf("chown to another identity was admitted: %+v", r.result)
	}
}

func tarEntries(t *testing.T, r io.Reader) map[string]string {
	t.Helper()
	tr := tar.NewReader(r)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		testutil.FailErr(t, "read tar", err)
		data, err := io.ReadAll(tr)
		testutil.FailErr(t, "read entry", err)
		out[hdr.Name] = string(data)
	}
}

func TestTarCreateListAndExtract(t *testing.T) {
	h := newHarness(t, map[string]string{"proj/a.txt": "alpha\n", "proj/sub/b.txt": "beta\n"})
	for _, c := range []struct{ flag, name string }{{"-z", "out.tgz"}, {"-j", "out.tbz2"}, {"", "out.tar"}} {
		create := []string{"tar", "-c", "-f", c.name, "proj"}
		if c.flag != "" {
			create = []string{"tar", "-c", c.flag, "-f", c.name, "proj"}
		}
		if r := h.run("", create...); r.err != nil || r.result.Status != 0 {
			t.Fatalf("%v: %+v %v", create, r.result, r.err)
		}
		raw, err := os.ReadFile(h.path(c.name))
		testutil.FailErr(t, "read archive", err)
		// Cross-decode with the standard library readers.
		var stream io.Reader = bytes.NewReader(raw)
		switch c.flag {
		case "-z":
			gz, err := gzip.NewReader(stream)
			testutil.FailErr(t, "gzip reader", err)
			stream = gz
		case "-j":
			stream = bzip2.NewReader(stream)
		}
		entries := tarEntries(t, stream)
		if entries["proj/a.txt"] != "alpha\n" || entries["proj/sub/b.txt"] != "beta\n" {
			t.Fatalf("%s entries %v", c.name, entries)
		}
		list := h.run("", "tar", "-t", "-f", c.name)
		if list.err != nil || !strings.Contains(list.stdout, "proj/sub/b.txt\n") {
			t.Fatalf("tar -t %q %v", list.stdout, list.err)
		}
		out := "x" + strings.TrimPrefix(filepath.Ext(c.name), ".")
		if r := h.run("", "mkdir", out); r.err != nil {
			t.Fatal(r.err)
		}
		if r := h.run("", "tar", "-x", "-f", c.name, "-C", out, "--strip-components", "1"); r.err != nil || r.result.Status != 0 {
			t.Fatalf("tar -x %+v %v", r.result, r.err)
		}
		if h.read(out+"/a.txt") != "alpha\n" || h.read(out+"/sub/b.txt") != "beta\n" {
			t.Fatalf("%s extraction mismatch", c.name)
		}
	}
}

func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for name, content := range entries {
		w, err := zw.Create(name)
		testutil.FailErr(t, "zip entry", err)
		_, _ = io.WriteString(w, content)
	}
	testutil.FailErr(t, "zip close", zw.Close())
	testutil.FailErr(t, "write zip", os.WriteFile(path, out.Bytes(), 0o644))
}

func TestZipAndUnzip(t *testing.T) {
	h := newHarness(t, map[string]string{"docs/one.md": "one\n", "docs/two.md": "two\n"})
	if r := h.run("", "zip", "-r", "-q", "docs.zip", "docs"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("zip %+v %v", r.result, r.err)
	}
	list := h.run("", "unzip", "-l", "docs.zip")
	if list.err != nil || !strings.Contains(list.stdout, "docs/two.md") || !strings.Contains(list.stdout, "3 files") {
		t.Fatalf("unzip -l %q %v", list.stdout, list.err)
	}
	if pipe := h.run("", "unzip", "-p", "docs.zip", "docs/one.md"); pipe.err != nil || pipe.stdout != "one\n" {
		t.Fatalf("unzip -p %q %v", pipe.stdout, pipe.err)
	}
	if r := h.run("", "mkdir", "out"); r.err != nil {
		t.Fatal(r.err)
	}
	if r := h.run("", "unzip", "-q", "-d", "out", "docs.zip"); r.err != nil || h.read("out/docs/two.md") != "two\n" {
		t.Fatalf("unzip -d %+v %v", r.result, r.err)
	}
	testutil.FailErr(t, "local edit", os.WriteFile(h.path("out/docs/one.md"), []byte("edited\n"), 0o644))
	if r := h.run("", "unzip", "-q", "-d", "out", "docs.zip"); r.result.Status != 1 || h.read("out/docs/one.md") != "edited\n" {
		t.Fatalf("unzip replaced without -o: %+v %q", r.result, r.stderr)
	}
	if r := h.run("", "unzip", "-o", "-q", "-d", "out", "docs.zip"); r.err != nil || h.read("out/docs/one.md") != "one\n" {
		t.Fatalf("unzip -o %+v %v", r.result, r.err)
	}
}

func TestHostileArchivesRefuseFilesystemEffectsAndBoundOutput(t *testing.T) {
	h := newHarness(t, map[string]string{"out/": ""})
	writeZip(t, h.path("slip.zip"), map[string]string{"../escape.txt": "x"})
	if r := h.run("", "unzip", "-q", "-d", "out", "slip.zip"); r.result.Terminal != toolcontract.ProducerRefused || !h.absent("escape.txt") {
		t.Fatalf("zip slip admitted: %+v", r.result)
	}
	var tarred bytes.Buffer
	tw := tar.NewWriter(&tarred)
	testutil.FailErr(t, "link header", tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}))
	testutil.FailErr(t, "tar close", tw.Close())
	testutil.FailErr(t, "write tar", os.WriteFile(h.path("link.tar"), tarred.Bytes(), 0o644))
	if r := h.run("", "tar", "-x", "-f", "link.tar", "-C", "out"); r.result.Terminal != toolcontract.ProducerRefused || !h.absent("out/link") {
		t.Fatalf("archive link extracted: %+v", r.result)
	}
	// Streaming output may precede refusal, but never exceeds the expansion ceiling.
	bomb := gzipBytes(t, make([]byte, h.owners.limit("archive_extract_bytes")+(1<<20)))
	testutil.FailErr(t, "write bomb", os.WriteFile(h.path("bomb.gz"), bomb, 0o644))
	r := h.run("", "zcat", "bomb.gz")
	reject := toolrejection.AsToolReject(r.err)
	maximum := min(h.owners.limit("archive_extract_bytes"), int64(len(bomb))*h.owners.limit("archive_expansion_ratio"))
	if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || len(r.stdout) == 0 || int64(len(r.stdout)) > maximum || r.result.EffectCertainty != toolcontract.NoEffect {
		t.Fatalf("bomb decoded: %v (%d bytes out), result=%+v", r.err, len(r.stdout), r.result)
	}
	if reject.Data["maximum"] != maximum || strings.Trim(r.stdout, "\x00") != "" {
		t.Fatalf("wrong limit or changed stream bytes: %+v", reject.Data)
	}
}

func TestGzipGunzipAndZcatRoundTrip(t *testing.T) {
	h := newHarness(t, map[string]string{"log.txt": "line\nline\n"})
	if r := h.run("", "gzip", "log.txt"); r.err != nil || r.result.Status != 0 || !h.absent("log.txt") {
		t.Fatalf("gzip %+v %v", r.result, r.err)
	}
	if r := h.run("", "zcat", "log.txt.gz"); r.err != nil || r.stdout != "line\nline\n" {
		t.Fatalf("zcat %q %v", r.stdout, r.err)
	}
	compressed, err := os.ReadFile(h.path("log.txt.gz"))
	testutil.FailErr(t, "read gz", err)
	if r := h.run(string(compressed), "zcat"); r.err != nil || r.stdout != "line\nline\n" {
		t.Fatalf("zcat stdin %q %v", r.stdout, r.err)
	}
	if r := h.run("", "gunzip", "log.txt.gz"); r.err != nil || h.read("log.txt") != "line\nline\n" || !h.absent("log.txt.gz") {
		t.Fatalf("gunzip %+v %v", r.result, r.err)
	}
	testutil.FailErr(t, "seed existing", os.WriteFile(h.path("keep.txt.gz"), gzipBytes(t, []byte("new")), 0o644))
	testutil.FailErr(t, "seed target", os.WriteFile(h.path("keep.txt"), []byte("existing"), 0o644))
	if r := h.run("", "gunzip", "keep.txt.gz"); r.result.Status != 1 || h.read("keep.txt") != "existing" {
		t.Fatalf("gunzip replaced an existing file: %+v", r.result)
	}
}

// The recursive caps come from the catalog by default; the seam injects small
// ceilings so the boundary and one past it are exercised without huge trees.
func TestRecursiveMutationCapsAtBoundaryAndOnePast(t *testing.T) {
	catalog, err := native.CatalogRecursiveCaps()
	testutil.FailErr(t, "catalog caps", err)
	language, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	for _, limit := range language.Language.Limits {
		if (limit.ID == "recursive_mutation_files" && limit.Maximum != catalog.Files) || (limit.ID == "recursive_mutation_bytes" && limit.Maximum != catalog.Bytes) {
			t.Fatalf("default caps %+v differ from catalog %s", catalog, limit.ID)
		}
	}
	tree := func(files int, size int) map[string]string {
		out := map[string]string{}
		for i := 0; i < files; i++ {
			out[fmt.Sprintf("tree/d%d/f%d.txt", i%2, i)] = strings.Repeat("x", size)
		}
		return out
	}
	caps := native.RecursiveCaps{Files: 4, Bytes: 40}
	cases := []struct {
		name        string
		files, size int
		limit       string
	}{
		{"files and bytes at limit", 4, 10, ""},
		{"one file past", 5, 1, "recursive_mutation_files"},
		{"one byte past", 3, 14, "recursive_mutation_bytes"},
	}
	for _, c := range cases {
		for _, command := range [][]string{{"rm", "-rf", "tree"}, {"cp", "-r", "tree", "copy"}} {
			t.Run(c.name+" "+command[0], func(t *testing.T) {
				h := newHarness(t, tree(c.files, c.size))
				h.owners.Operations.Copy.Caps = caps
				if command[0] == "rm" {
					if c.limit == "recursive_mutation_bytes" {
						h.service.Effects.SetSameFilesystem(func(string, string) (bool, error) { return false, nil })
					}
				}
				var reviewed []tools.FileChange
				h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
					reviewed = append(reviewed, changes...)
					return nil
				}
				r := h.run("", command...)
				reject := toolrejection.AsToolReject(r.err)
				if command[0] == "rm" {
					if r.err != nil || r.result.Status != 0 || !h.absent("tree") {
						t.Fatalf("whole-tree removal failed: %+v %v", r.result, r.err)
					}
					permanent := c.limit == "recursive_mutation_bytes"
					if len(reviewed) != 1 || reviewed[0].RecoveryLoss != permanent {
						t.Fatalf("recovery-loss decision = %+v, want %v", reviewed, permanent)
					}
					history, err := h.service.History.State(t.Context(), h.project.ID)
					testutil.FailErr(t, "read history", err)
					if (history.Undo == nil) != permanent {
						t.Fatalf("whole-tree recovery = %+v, permanent=%v", history.Undo, permanent)
					}
					return
				}
				if c.limit == "" {
					if r.err != nil || r.result.Status != 0 {
						t.Fatalf("at the copy limit refused: %+v %v", r.result, r.err)
					}
					return
				}
				if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["limit_id"] != c.limit || r.result.EffectCertainty != toolcontract.NoEffect {
					t.Fatalf("one past: %+v %v", r.result, r.err)
				}
				if h.absent("tree") || !h.absent("copy") {
					t.Fatal("refused recursive mutation had an effect")
				}
			})
		}
	}
}

func paddedGzipBytes(t *testing.T, payloadLen, targetGzLen int) []byte {
	t.Helper()
	payload := bytes.Repeat([]byte("a"), payloadLen)
	build := func(commentLen int) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if commentLen > 0 {
			gz.Comment = strings.Repeat("c", commentLen)
		}
		_, err := gz.Write(payload)
		testutil.FailErr(t, "gzip write", err)
		testutil.FailErr(t, "gzip close", gz.Close())
		return buf.Bytes()
	}
	base := build(0)
	if len(base) > targetGzLen {
		t.Fatalf("base gzip len %d > target %d", len(base), targetGzLen)
	}
	if len(base) == targetGzLen {
		return base
	}
	// A non-empty gzip comment adds len(comment)+1 bytes (null-terminated).
	got := build(targetGzLen - len(base) - 1)
	if len(got) != targetGzLen {
		t.Fatalf("padded gzip len %d, want %d", len(got), targetGzLen)
	}
	return got
}

func TestArchiveCeilingsAtBoundaryAndOnePast(t *testing.T) {
	h := newHarness(t, nil)
	ratio := h.owners.limit("archive_expansion_ratio")
	maxEntries := h.owners.limit("archive_extract_entries")
	maxBytes := h.owners.limit("archive_extract_bytes")
	if ratio != 100 || maxEntries != 25_000 || maxBytes != 128<<20 {
		t.Fatalf("limits ratio=%d entries=%d bytes=%d", ratio, maxEntries, maxBytes)
	}

	// 1. archive_expansion_ratio = 100 via zcat: 100-byte .gz expanding to 10,000 bytes succeeds; 10,001 refuses.
	testutil.FailErr(t, "write at-ratio.gz", os.WriteFile(h.path("at-ratio.gz"), paddedGzipBytes(t, int(100*ratio), 100), 0o644))
	if r := h.run("", "zcat", "at-ratio.gz"); r.err != nil || len(r.stdout) != int(100*ratio) {
		t.Fatalf("zcat at 100x ratio: err=%v len=%d", r.err, len(r.stdout))
	}
	testutil.FailErr(t, "write past-ratio.gz", os.WriteFile(h.path("past-ratio.gz"), paddedGzipBytes(t, int(100*ratio)+1, 100), 0o644))
	if r := h.run("", "zcat", "past-ratio.gz"); toolrejection.AsToolReject(r.err) == nil || toolrejection.AsToolReject(r.err).Code != "COMMAND_LIMIT_EXCEEDED" {
		t.Fatalf("zcat one byte past 100x ratio: %v", r.err)
	}

	// 2. archive_extract_entries = 25,000 via tar -tf: 25,000 entries succeeds; 25,001 refuses.
	writeEmptyEntryTar := func(name string, count int) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for i := range count {
			testutil.FailErr(t, "tar header", tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%05d", i), Mode: 0o644, Size: 0, Typeflag: tar.TypeReg}))
		}
		testutil.FailErr(t, "tar close", tw.Close())
		testutil.FailErr(t, "write tar", os.WriteFile(h.path(name), buf.Bytes(), 0o644))
	}
	writeEmptyEntryTar("entries-at.tar", int(maxEntries))
	if r := h.run("", "tar", "-t", "-f", "entries-at.tar"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("tar -tf at 25,000 entries: %+v %v", r.result, r.err)
	}
	writeEmptyEntryTar("entries-past.tar", int(maxEntries)+1)
	r := h.run("", "tar", "-t", "-f", "entries-past.tar")
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["limit_id"] != "archive_extract_entries" {
		t.Fatalf("tar -tf at 25,001 entries: %+v %v", r.result, r.err)
	}
}

func TestTarAndGzipInteroperateWithGNU(t *testing.T) {
	var gtar string
	for _, candidate := range []string{"/opt/homebrew/bin/gtar", "/usr/local/bin/gtar"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			gtar = candidate
			break
		}
	}
	if gtar == "" {
		var err error
		if gtar, err = exec.LookPath("gtar"); err != nil {
			t.Skip("no gtar on this host")
		}
	}
	runGTar := func(dir string, args ...string) string {
		cmd := exec.Command(gtar, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
		out, err := cmd.Output()
		testutil.FailErr(t, "gtar "+strings.Join(args, " "), err)
		return string(out)
	}
	h := newHarness(t, map[string]string{"proj/a.txt": "alpha\n", "proj/sub/b.txt": "beta\n"})
	if r := h.run("", "tar", "-c", "-f", "native.tar", "proj"); r.err != nil {
		t.Fatalf("tar -cf: %v", r.err)
	}
	wantList := runGTar(h.root, "-t", "-f", "native.tar")
	if r := h.run("", "tar", "-t", "-f", "native.tar"); r.err != nil || r.stdout != wantList {
		t.Fatalf("tar -tf:\n got %q\nwant %q (%v)", r.stdout, wantList, r.err)
	}
	runGTar(h.root, "--sort=name", "-c", "-z", "-f", "gnu.tgz", "proj")
	if r := h.run("", "tar", "-t", "-f", "gnu.tgz"); r.err != nil || r.stdout != wantList {
		t.Fatalf("tar -tf gnu.tgz:\n got %q\nwant %q (%v)", r.stdout, wantList, r.err)
	}
	testutil.FailErr(t, "mkdir out", os.Mkdir(h.path("out"), 0o755))
	if r := h.run("", "tar", "-x", "-z", "-f", "gnu.tgz", "-C", "out", "--strip-components", "1"); r.err != nil || h.read("out/a.txt") != "alpha\n" || h.read("out/sub/b.txt") != "beta\n" {
		t.Fatalf("tar -xzf gnu.tgz: %+v %v", r.result, r.err)
	}
	if r := h.run("", "gzip", "proj/a.txt"); r.err != nil {
		t.Fatalf("gzip: %v", r.err)
	} else if _, err := os.Stat("/usr/bin/gzip"); err == nil {
		out, err := exec.Command("/usr/bin/gzip", "-d", "-c", h.path("proj/a.txt.gz")).Output()
		testutil.FailErr(t, "/usr/bin/gzip -dc", err)
		if string(out) != "alpha\n" {
			t.Fatalf("/usr/bin/gzip -dc = %q, want alpha\\n", string(out))
		}
	}
}
