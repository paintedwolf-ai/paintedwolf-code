//go:build darwin || linux

package projectpaths_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// TestBoundMetadataReadsEntriesThroughTheHeldRoot: directory listings are
// the directory itself in byte order, children keep the binding's authority,
// and links are described without being followed.
func TestBoundMetadataReadsEntriesThroughTheHeldRoot(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "seed listing", os.MkdirAll(filepath.Join(root, "list", "sub"), 0o755))
	for _, name := range []string{"b", ".hidden", "Z", "a"} {
		testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(root, "list", name), []byte(name), 0o644))
	}
	testutil.FailErr(t, "seed link", os.Symlink("a", filepath.Join(root, "list", "link")))
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	dir, err := p.Bind(t.Context(), "list", false)
	testutil.FailErr(t, "bind list", err)
	names, err := dir.ReadDir()
	testutil.FailErr(t, "read dir", err)
	if !slices.Equal(names, []string{".hidden", "Z", "a", "b", "link", "sub"}) {
		t.Fatalf("names = %v", names)
	}
	child := dir.Child("sub").Child("..").Child("a")
	if child.Resolved.DisplayPath != "list/a" {
		t.Fatalf("child display = %q", child.Resolved.DisplayPath)
	}
	st, err := dir.Child("a").Stat(false)
	testutil.FailErr(t, "stat child", err)
	if st.Size != 1 || st.Name != "a" {
		t.Fatalf("child = %+v", st)
	}
	link := dir.Child("link")
	lst, err := link.Stat(false)
	testutil.FailErr(t, "lstat link", err)
	followed, err := link.Stat(true)
	testutil.FailErr(t, "follow link", err)
	if lst.Mode&os.ModeSymlink == 0 || followed.Mode&os.ModeSymlink != 0 || followed.Size != 1 {
		t.Fatalf("link = %+v followed %+v", lst, followed)
	}
	target, err := link.Readlink()
	if err != nil || target != "a" {
		t.Fatalf("readlink = %q %v", target, err)
	}
	file, err := p.Bind(t.Context(), "list/a", false)
	testutil.FailErr(t, "bind file", err)
	if _, err := file.ReadDir(); err == nil {
		t.Fatal("listed a regular file")
	}
	null, err := p.Bind(t.Context(), os.DevNull, false)
	testutil.FailErr(t, "bind null", err)
	if _, err := null.Stat(false); err == nil {
		t.Fatal("stat of the null device succeeded")
	}
	if _, err := null.Readlink(); err == nil {
		t.Fatal("readlink of the null device succeeded")
	}
}

// TestReadSessionObserverSeesOpensBeforeContent: an observer sees each open
// with its file and can refuse it.
func TestReadSessionObserverSeesOpensBeforeContent(t *testing.T) {
	tc, _ := readSessionContext(t)
	reads := projectpaths.NewReadSession(nil, tc)
	defer reads.Close()
	resolved, err := reads.Resolve(t.Context(), "a.go")
	testutil.FailErr(t, "resolve", err)
	var seen []string
	reads.Observe(func(r projectpaths.Resolved, f *os.File) error {
		seen = append(seen, r.DisplayPath)
		return nil
	})
	file, err := reads.Open(resolved)
	testutil.FailErr(t, "observed open", err)
	file.Close()
	if !slices.Equal(seen, []string{"a.go"}) {
		t.Fatalf("observed = %v", seen)
	}
	refused := errors.New("refused")
	reads.Observe(func(projectpaths.Resolved, *os.File) error { return refused })
	if _, err := reads.Open(resolved); !errors.Is(err, refused) {
		t.Fatalf("refused open = %v", err)
	}
}
