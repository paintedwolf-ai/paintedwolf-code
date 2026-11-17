package mutation

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// archiveEntry is one member: f file, d directory, l symbolic link, h hard link.
type archiveEntry struct {
	kind         byte
	name, target string
}

// archiveEntries reads one member per line: a kind byte, the name, and for a
// link a NUL then its target.
func archiveEntries(spec string) []archiveEntry {
	var entries []archiveEntry
	for _, line := range strings.Split(spec, "\n") {
		if line == "" || len(entries) == 16 {
			continue
		}
		name, target, _ := strings.Cut(line[1:], "\x00")
		entries = append(entries, archiveEntry{kind: "fdlh"[int(line[0])%4], name: name, target: target})
	}
	return entries
}

func tarArchive(t *testing.T, entries []archiveEntry) []byte {
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for _, e := range entries {
		header := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: tar.TypeReg, Size: 1}
		switch e.kind {
		case 'd':
			header = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		case 'l':
			header = &tar.Header{Name: e.name, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: e.target}
		case 'h':
			header = &tar.Header{Name: e.name, Mode: 0o644, Typeflag: tar.TypeLink, Linkname: e.target}
		}
		if err := tw.WriteHeader(header); err != nil {
			return nil
		}
		if header.Size > 0 {
			_, err := tw.Write([]byte("x"))
			testutil.FailErr(t, "tar body", err)
		}
	}
	testutil.FailErr(t, "tar close", tw.Close())
	return out.Bytes()
}

func zipArchive(t *testing.T, entries []archiveEntry) []byte {
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, e := range entries {
		header := &zip.FileHeader{Name: e.name, Method: zip.Store}
		body := "x"
		switch e.kind {
		case 'd':
			header.Name = strings.TrimSuffix(e.name, "/") + "/"
			header.SetMode(fs.ModeDir | 0o755)
		case 'l', 'h':
			header.SetMode(fs.ModeSymlink | 0o777)
			body = e.target
		default:
			header.SetMode(0o644)
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			return nil
		}
		if !strings.HasSuffix(header.Name, "/") {
			_, err = w.Write([]byte(body))
			testutil.FailErr(t, "zip body", err)
		}
	}
	testutil.FailErr(t, "zip close", zw.Close())
	return out.Bytes()
}

// treeState records every entry under dir without following links: its kind
// and, for a link its target, for a file its bytes.
func treeState(t *testing.T, dir string, skip string) map[string]string {
	state := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == skip {
			return filepath.SkipDir
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			state[path] = "link " + target
			return err
		case info.IsDir():
			state[path] = "dir"
		default:
			data, err := os.ReadFile(path)
			state[path] = "file " + string(data)
			return err
		}
		return nil
	})
	testutil.FailErr(t, "walk "+dir, err)
	return state
}

// resetArchiveTree lays out a destination holding links that lead out of it:
// to a folder outside the project, to a sibling, dangling into the sibling,
// and into an outside folder whose own link is relative and dangling.
func resetArchiveTree(t *testing.T, root, outside string) {
	for _, dir := range []string{"dest", "sibling", "in.tar", "in.zip"} {
		testutil.FailErr(t, "clear "+dir, os.RemoveAll(filepath.Join(root, dir)))
	}
	entries, err := os.ReadDir(outside)
	testutil.FailErr(t, "read outside", err)
	for _, entry := range entries {
		testutil.FailErr(t, "clear outside", os.RemoveAll(filepath.Join(outside, entry.Name())))
	}
	for _, dir := range []string{filepath.Join(root, "dest", "a", "b"), filepath.Join(root, "sibling"), filepath.Join(outside, "shallow")} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
	}
	for path, content := range map[string]string{filepath.Join(root, "sibling", "keep.txt"): "keep", filepath.Join(root, "dest", "inside.txt"): "in", filepath.Join(outside, "secret"): "secret"} {
		testutil.FailErr(t, "write", os.WriteFile(path, []byte(content), 0o600))
	}
	for link, target := range map[string]string{
		filepath.Join(root, "dest", "esc"): outside, filepath.Join(root, "dest", "up"): "../sibling",
		filepath.Join(root, "dest", "dangle"): "../sibling/new", filepath.Join(root, "dest", "a", "b", "c"): filepath.Join(outside, "shallow"),
		filepath.Join(outside, "shallow", "d"): "../x",
	} {
		testutil.FailErr(t, "link", os.Symlink(target, link))
	}
}

// FuzzArchiveExtractionStaysInDestination extracts arbitrary member names,
// kinds and link targets with tar and unzip into a destination that already
// holds links leading out of it, and requires that nothing outside the
// destination changes and that extraction creates no link inside it.
func FuzzArchiveExtractionStaysInDestination(f *testing.F) {
	for _, seed := range []string{
		"fa.txt\ndsub\nfsub/b.txt",
		"f../escape.txt\nf/abs.txt\nfsub/../../escape.txt",
		"fesc/planted\nfup/planted\nfdangle\nfa/b/c/d",
		"llink\x00../../outside\nflink/planted",
		"hhard\x00../keep.txt\nl./self\x00/etc",
		"d./\nf./x/./y\nf\\..\\escape.txt\nf a/ b",
	} {
		f.Add(seed, uint8(0), false, false)
		f.Add(seed, uint8(1), true, true)
	}
	h := newHarness(f, nil)
	outside, err := filepath.EvalSymlinks(f.TempDir())
	testutil.FailErr(f, "resolve outside", err)
	f.Fuzz(func(t *testing.T, spec string, strip uint8, zipped, flag bool) {
		entries := archiveEntries(spec)
		if len(entries) == 0 {
			return
		}
		h.t = t
		resetArchiveTree(t, h.root, outside)
		// flag lists names with tar -v, or overwrites with unzip -o; strip's low bit flattens with unzip -j.
		archive, args := "in.tar", []string{"tar", "-xf", "in.tar", "-C", "dest", "--strip-components", strconv.Itoa(int(strip % 3))}
		data := tarArchive(t, entries)
		if flag {
			args = append(args, "-v")
		}
		if zipped {
			archive, args = "in.zip", []string{"unzip", "-q", "-d", "dest", "in.zip"}
			if flag {
				args = append(args, "-o")
			}
			if strip%2 == 1 {
				args = append(args, "-j")
			}
			data = zipArchive(t, entries)
		}
		if data == nil {
			return
		}
		testutil.FailErr(t, "write archive", os.WriteFile(h.path(archive), data, 0o644))
		dest := h.path("dest")
		beforeRoot, beforeOutside, beforeDest := treeState(t, h.root, dest), treeState(t, outside, ""), treeState(t, dest, "")

		_ = h.run("", args...)

		if after := treeState(t, h.root, dest); !maps.Equal(beforeRoot, after) {
			t.Fatalf("%v changed the project outside the destination:\nbefore %v\nafter  %v", args, beforeRoot, after)
		}
		if after := treeState(t, outside, ""); !maps.Equal(beforeOutside, after) {
			t.Fatalf("%v changed a folder outside the project:\nbefore %v\nafter  %v", args, beforeOutside, after)
		}
		for path, state := range treeState(t, dest, "") {
			if strings.HasPrefix(state, "link ") && beforeDest[path] != state {
				t.Fatalf("%v created link %s (%s)", args, path, state)
			}
		}
	})
}
