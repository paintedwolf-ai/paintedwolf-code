package mutation

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// projectTree is a package layout with dependencies, version control files,
// build output and a link inside the dependency tree.
func projectTree(t *testing.T) *harness {
	h := newHarness(t, map[string]string{
		"proj/README": "readme\n", "proj/src/main.go": "package main\n", "proj/src/.gitignore": "bin\n",
		"proj/node_modules/pkg/index.js": "x\n", "proj/.git/HEAD": "ref\n", "proj/build/x.o": "obj\n", "proj/build/keep.txt": "keep\n",
	})
	testutil.FailErr(t, "dependency link", os.Symlink("../pkg/index.js", h.path("proj/node_modules/tool")))
	return h
}

func names(entries map[string]string) []string {
	out := make([]string, 0, len(entries))
	for name := range entries {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func gunzipped(t *testing.T, raw []byte) io.Reader {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	testutil.FailErr(t, "gzip reader", err)
	return gz
}

func TestTarExclusionsLeaveOutTreesBeforeExaminingThem(t *testing.T) {
	h := projectTree(t)
	if r := h.run("", "tar", "-czf", "out.tgz", "--exclude=node_modules", "--exclude-vcs", "--exclude", "*.o", "-C", "proj", "."); r.err != nil || r.result.Status != 0 {
		t.Fatalf("tar -czf with exclusions: %+v %v %s", r.result, r.err, r.stderr)
	}
	raw, err := os.ReadFile(h.path("out.tgz"))
	testutil.FailErr(t, "read archive", err)
	if got := names(tarEntries(t, gunzipped(t, raw))); !slices.Equal(got, []string{"./", "README", "build/", "build/keep.txt", "src/", "src/main.go"}) {
		t.Fatalf("archived %q", got)
	}
	testutil.FailErr(t, "exclusion file", os.WriteFile(h.path("exclude.lst"), []byte("build\nnode_modules\n.git\n"), 0o644))
	testutil.FailErr(t, "name file", os.WriteFile(h.path("names.txt"), []byte("proj/README\nproj/src/main.go\n"), 0o644))
	if r := h.run("", "tar", "-cf", "listed.tar", "-X", "exclude.lst", "proj"); r.err != nil {
		t.Fatalf("tar -X: %v", r.err)
	}
	raw, err = os.ReadFile(h.path("listed.tar"))
	testutil.FailErr(t, "read -X archive", err)
	if got := names(tarEntries(t, bytes.NewReader(raw))); !slices.Equal(got, []string{"proj/", "proj/README", "proj/src/", "proj/src/.gitignore", "proj/src/main.go"}) {
		t.Fatalf("-X archived %q", got)
	}
	if r := h.run("", "tar", "--files-from=names.txt", "-cf", "named.tar"); r.err != nil {
		t.Fatalf("tar -T: %v", r.err)
	}
	if list := h.run("", "tar", "-tf", "named.tar"); list.err != nil || list.stdout != "proj/README\nproj/src/main.go\n" {
		t.Fatalf("tar -T archive lists %q %v", list.stdout, list.err)
	}
	if list := h.run("", "tar", "-tzf", "out.tgz", "--exclude=src"); list.err != nil || list.stdout != "./\nREADME\nbuild/\nbuild/keep.txt\n" {
		t.Fatalf("tar -t --exclude lists %q %v", list.stdout, list.err)
	}
	if r := h.run("", "mkdir", "x"); r.err != nil {
		t.Fatal(r.err)
	}
	if r := h.run("", "tar", "-xzf", "out.tgz", "-C", "x", "--exclude=README"); r.err != nil || !h.absent("x/README") || h.read("x/src/main.go") != "package main\n" {
		t.Fatalf("tar -x --exclude: %+v %v", r.result, r.err)
	}
	if r := h.run("", "tar", "-czf", "linked.tgz", "proj"); toolrejection.AsToolReject(r.err) == nil || toolrejection.AsToolReject(r.err).Code != "COMMAND_NATIVE_FORM_UNSUPPORTED" {
		t.Fatalf("an unexcluded link was archived: %+v %v", r.result, r.err)
	}
}

func TestTarXZAndZstdRoundTrip(t *testing.T) {
	decoders := map[string]func(*testing.T, []byte) io.Reader{
		"tar.xz": func(t *testing.T, raw []byte) io.Reader {
			r, err := xz.NewReader(bytes.NewReader(raw))
			testutil.FailErr(t, "xz reader", err)
			return r
		},
		"tar.zst": func(t *testing.T, raw []byte) io.Reader {
			r, err := zstd.NewReader(bytes.NewReader(raw))
			testutil.FailErr(t, "zstd reader", err)
			return r
		},
		"tar.gz": gunzipped,
	}
	for _, c := range []struct {
		create []string
		format string
	}{
		{[]string{"tar", "-cJf", "out.tar.xz", "proj"}, "tar.xz"},
		{[]string{"tar", "--xz", "-c", "-f", "out.txz", "proj"}, "tar.xz"},
		{[]string{"tar", "--zstd", "-cf", "out.tar.zst", "proj"}, "tar.zst"},
		{[]string{"tar", "-caf", "out.tzst", "proj"}, "tar.zst"},
		{[]string{"tar", "--auto-compress", "-cf", "out.tar.gz", "proj"}, "tar.gz"},
	} {
		h := newHarness(t, map[string]string{"proj/a.txt": "alpha\n", "proj/sub/b.txt": "beta\n"})
		archive := c.create[len(c.create)-2]
		if r := h.run("", c.create...); r.err != nil || r.result.Status != 0 {
			t.Fatalf("%v: %+v %v", c.create, r.result, r.err)
		}
		raw, err := os.ReadFile(h.path(archive))
		testutil.FailErr(t, "read "+archive, err)
		if entries := tarEntries(t, decoders[c.format](t, raw)); entries["proj/a.txt"] != "alpha\n" || entries["proj/sub/b.txt"] != "beta\n" {
			t.Fatalf("%v wrote %v", c.create, names(entries))
		}
		if list := h.run("", "tar", "-tf", archive); list.err != nil || list.stdout != "proj/\nproj/a.txt\nproj/sub/\nproj/sub/b.txt\n" {
			t.Fatalf("sniffed listing of %s: %q %v", archive, list.stdout, list.err)
		}
		for _, dir := range []string{"sniffed", "named"} {
			if r := h.run("", "mkdir", dir); r.err != nil {
				t.Fatal(r.err)
			}
		}
		if r := h.run("", "tar", "-xf", archive, "-C", "sniffed"); r.err != nil || h.read("sniffed/proj/sub/b.txt") != "beta\n" {
			t.Fatalf("sniffed extraction of %s: %+v %v", archive, r.result, r.err)
		}
		codec := map[string]string{"tar.xz": "-J", "tar.zst": "--zstd", "tar.gz": "-z"}[c.format]
		if r := h.run("", "tar", "-x", codec, "-f", archive, "-C", "named", "--strip-components=1"); r.err != nil || h.read("named/a.txt") != "alpha\n" {
			t.Fatalf("tar -x %s of %s: %+v %v", codec, archive, r.result, r.err)
		}
	}
}

func TestCompressedTarExtractionKeepsEveryGuard(t *testing.T) {
	plain := func(t *testing.T, name string, body []byte) []byte {
		var out bytes.Buffer
		tw := tar.NewWriter(&out)
		testutil.FailErr(t, "header", tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write(body)
		testutil.FailErr(t, "body", err)
		testutil.FailErr(t, "close", tw.Close())
		return out.Bytes()
	}
	encode := map[string]func(*testing.T, []byte) []byte{
		"-J": func(t *testing.T, data []byte) []byte {
			var out bytes.Buffer
			w, err := xz.NewWriter(&out)
			testutil.FailErr(t, "xz writer", err)
			_, err = w.Write(data)
			testutil.FailErr(t, "xz write", err)
			testutil.FailErr(t, "xz close", w.Close())
			return out.Bytes()
		},
		"--zstd": func(t *testing.T, data []byte) []byte {
			w, err := zstd.NewWriter(nil)
			testutil.FailErr(t, "zstd writer", err)
			return w.EncodeAll(data, nil)
		},
	}
	for flag, enc := range encode {
		h := newHarness(t, map[string]string{"dest/": ""})
		testutil.FailErr(t, "write slip", os.WriteFile(h.path("slip.tar"), enc(t, plain(t, "../escape.txt", []byte("x"))), 0o644))
		r := h.run("", "tar", "-x", flag, "-f", "slip.tar", "-C", "dest")
		if reject := toolrejection.AsToolReject(r.err); reject == nil || reject.Code != "EXTRACT_ZIP_SLIP" || r.result.Terminal != toolcontract.ProducerRefused || !h.absent("escape.txt") {
			t.Fatalf("%s traversal member: %+v %v", flag, r.result, r.err)
		}
		testutil.FailErr(t, "write bomb", os.WriteFile(h.path("bomb.tar"), enc(t, plain(t, "zeros", make([]byte, 4<<20))), 0o644))
		r = h.run("", "tar", "-xf", "bomb.tar", "-C", "dest")
		if reject := toolrejection.AsToolReject(r.err); reject == nil || reject.Code != "EXTRACT_SIZE_EXCEEDED" || !h.absent("dest/zeros") {
			t.Fatalf("%s expansion bomb: %+v %v", flag, r.result, r.err)
		}
		r = h.run("", "tar", "-tf", "bomb.tar")
		if reject := toolrejection.AsToolReject(r.err); reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" {
			t.Fatalf("%s listing a bomb: %+v %v", flag, r.result, r.err)
		}
	}
}

func zipNames(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	testutil.FailErr(t, "open zip", err)
	defer func() { _ = zr.Close() }()
	var out []string
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	return out
}

func TestZipExclusionsAndJunkPaths(t *testing.T) {
	h := projectTree(t)
	if r := h.run("", "zip", "-rq", "out.zip", "proj", "-x", "proj/node_modules/*", "*.git*", "*.o"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("zip -x: %+v %v", r.result, r.err)
	}
	if got := zipNames(t, h.path("out.zip")); !slices.Equal(got, []string{"proj/", "proj/README", "proj/build/", "proj/build/keep.txt", "proj/src/", "proj/src/main.go"}) {
		t.Fatalf("zip -x stored %q", got)
	}
	if r := h.run("", "zip", "-j", "-q", "flat.zip", "proj/README", "proj/src/main.go"); r.err != nil {
		t.Fatalf("zip -j: %v", r.err)
	}
	if got := zipNames(t, h.path("flat.zip")); !slices.Equal(got, []string{"README", "main.go"}) {
		t.Fatalf("zip -j stored %q", got)
	}
	testutil.FailErr(t, "second README", os.WriteFile(h.path("proj/src/README"), []byte("two\n"), 0o644))
	if r := h.run("", "zip", "-jrq", "dup.zip", "proj/README", "proj/src", "-x", "*.go", "*.gitignore"); toolrejection.AsToolReject(r.err) == nil || !h.absent("dup.zip") {
		t.Fatalf("zip -j stored two files under one name: %+v %v", r.result, r.err)
	}
}

func TestUnzipSelectionFlattenAndNoClobber(t *testing.T) {
	h := newHarness(t, map[string]string{"out/": "", "flat/": ""})
	writeZip(t, h.path("rel.zip"), map[string]string{"pkg/a.go": "a\n", "pkg/b.go": "b\n", "docs/readme.md": "doc\n", "docs/api/x.md": "x\n"})
	if r := h.run("", "unzip", "-q", "rel.zip", "-x", "docs/*", "-d", "out"); r.err != nil || h.read("out/pkg/a.go") != "a\n" || !h.absent("out/docs") {
		t.Fatalf("unzip -x: %+v %v", r.result, r.err)
	}
	if r := h.run("", "unzip", "-j", "-d", "flat", "rel.zip", "docs/*"); r.err != nil || h.read("flat/x.md") != "x\n" || h.read("flat/readme.md") != "doc\n" || !h.absent("flat/docs") || !strings.Contains(r.stdout, "  inflating: x.md\n") {
		t.Fatalf("unzip -j: %+v %v %q", r.result, r.err, r.stdout)
	}
	testutil.FailErr(t, "local edit", os.WriteFile(h.path("out/pkg/a.go"), []byte("edited\n"), 0o644))
	if r := h.run("", "unzip", "-n", "-q", "-d", "out", "rel.zip", "pkg/*"); r.err != nil || r.result.Status != 0 || r.stderr != "" || h.read("out/pkg/a.go") != "edited\n" {
		t.Fatalf("unzip -n: %+v %v %q", r.result, r.err, r.stderr)
	}
	if list := h.run("", "unzip", "-l", "rel.zip", "pkg/*"); list.err != nil || !strings.Contains(list.stdout, "2 files") || strings.Contains(list.stdout, "docs/") {
		t.Fatalf("unzip -l with members %q %v", list.stdout, list.err)
	}
	if pipe := h.run("", "unzip", "-p", "rel.zip", "*.go", "-x", "pkg/b.go"); pipe.err != nil || pipe.stdout != "a\n" {
		t.Fatalf("unzip -p with -x %q %v", pipe.stdout, pipe.err)
	}
	if r := h.run("", "unzip", "-o", "-n", "rel.zip"); toolrejection.AsToolReject(r.err) == nil {
		t.Fatalf("unzip -o -n accepted: %+v", r.result)
	}
}
