//go:build darwin

package nfsdarwin_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	"github.com/lycaon/lycaon/internal/testutil"
)

func (v *mountedView) changeAt(t *testing.T, rel string) (layeredfs.ChangeEntry, bool) {
	t.Helper()
	for _, ch := range v.publishedChanges(t) {
		if ch.Path.Rel == rel {
			return ch, true
		}
	}
	return layeredfs.ChangeEntry{}, false
}

// TestKernelSidecarsLeaveTheChangeSetAndKeepEveryAttribute drives the system's xattr tool, whose
// attributes the kernel stores in real ._ files. Each file carries them as attributes instead, and
// each tool result keeps working through the kernel's held handles.
func TestKernelSidecarsLeaveTheChangeSetAndKeepEveryAttribute(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"lower.txt": "lower"})
	v.shell(t, `echo new > f; mkdir d; echo inner > d/g`)
	v.shell(t, `xattr -w user.one first f; xattr -w user.two second f; xattr -w user.one rewritten f`)
	v.shell(t, `xattr -w user.in deep d/g; xattr -w user.low trunk-file lower.txt; xattr -w user.dir on-dir d`)
	// Larger than one write, and within the 128 KiB the kernel allows a sidecar attribute.
	large := strings.Repeat("v", 100_000)
	v.shell(t, `printf %s "`+large+`" > value && xattr -w user.large "$(cat value)" f && rm value`)
	v.barrier(t)

	if got := v.shell(t, `xattr -p user.one f; xattr -p user.two f; xattr -p user.in d/g; xattr -p user.low lower.txt; xattr -p user.dir d`); got != "rewritten\nsecond\ndeep\ntrunk-file\non-dir\n" {
		t.Fatalf("attributes through the mount: %q", got)
	}
	if got := v.shell(t, `xattr -p user.large f`); strings.TrimSpace(got) != large {
		t.Fatalf("large attribute: %d bytes", len(got))
	}
	listing := v.shell(t, `find . -name '._*'; ls -a`)
	if strings.Contains(listing, "._") {
		t.Fatalf("a sidecar is listed: %q", listing)
	}
	for _, rel := range []string{"._f", "d/._g", "._lower.txt", "._d"} {
		if ch, found := v.changeAt(t, rel); found {
			t.Fatalf("%s is in the change set: %+v", rel, ch)
		}
	}
	file, found := v.changeAt(t, "f")
	if !found || string(file.Final.Xattrs["user.one"]) != "rewritten" || string(file.Final.Xattrs["user.large"]) != large {
		t.Fatalf("f did not publish its attributes: found=%v", found)
	}
	lower, found := v.changeAt(t, "lower.txt")
	if !found || string(lower.Final.Xattrs["user.low"]) != "trunk-file" {
		t.Fatalf("lower.txt did not publish its attribute: %+v", lower)
	}

	v.shell(t, `xattr -d user.two f; mv f renamed; cp -p renamed copied`)
	if got := v.shell(t, `xattr -p user.one renamed; xattr -p user.one copied`); got != "rewritten\nrewritten\n" {
		t.Fatalf("after rename and copy: %q", got)
	}
	if got := v.shell(t, `xattr renamed`); strings.Contains(got, "user.two") {
		t.Fatalf("a removed attribute came back: %q", got)
	}
	v.shell(t, `xattr -c renamed`)
	// The system keeps a provenance tag through xattr -c, as it does on a local volume.
	if got := v.shell(t, `xattr renamed`); strings.TrimSpace(strings.ReplaceAll(got, "com.apple.provenance", "")) != "" {
		t.Fatalf("after clearing: %q", got)
	}
	v.barrier(t)
	for _, rel := range []string{"._f", "._renamed", "._copied"} {
		if ch, found := v.changeAt(t, rel); found {
			t.Fatalf("%s is in the change set: %+v", rel, ch)
		}
	}
}

// TestSidecarNamedDataStaysDataThroughTheMount: the name and the magic bytes decide nothing. What a
// worker wrote as data is published exactly as written, while a complete file with a target is held.
func TestSidecarNamedDataStaysDataThroughTheMount(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"lower.txt": "lower"})
	valid := nfsserver.EncodeAppleDoubleXattrs([32]byte{}, map[string][]byte{"user.a": []byte("one")})
	testutil.FailErr(t, "write target", os.WriteFile(filepath.Join(v.mnt, "target"), []byte("body"), 0o644))

	undecodable := []byte("the worker's own bytes, not an AppleDouble file")
	magicOnly := append(bytes.Clone(valid[:8]), []byte("then nothing the kernel wrote")...)
	partial := bytes.Clone(valid[:200])
	for name, content := range map[string][]byte{"._target": undecodable, "._lower.txt": magicOnly, "._no-target": valid, "._renamed-away": partial} {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(v.mnt, name), content, 0o644))
	}
	testutil.FailErr(t, "rename", os.Rename(filepath.Join(v.mnt, "._renamed-away"), filepath.Join(v.mnt, "._moved")))
	v.barrier(t)

	want := map[string][]byte{"._target": undecodable, "._lower.txt": magicOnly, "._no-target": valid, "._moved": partial}
	for rel, content := range want {
		ch, found := v.changeAt(t, rel)
		if !found {
			t.Fatalf("%s left the change set", rel)
		}
		got, err := v.core.ReadFile(ch.Path)
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("%s content: %q err=%v", rel, got, err)
		}
		if read, err := os.ReadFile(filepath.Join(v.mnt, rel)); err != nil || !bytes.Equal(read, content) {
			t.Fatalf("%s through the mount: %q err=%v", rel, read, err)
		}
	}
	for _, rel := range []string{"target", "lower.txt"} {
		attr, err := v.core.Lstat(layeredfs.QualifiedPath{RootID: "primary", Rel: rel})
		testutil.FailErr(t, "lstat "+rel, err)
		// The system may tag a new file with its provenance, depending on what launched the test.
		delete(attr.Xattrs, "com.apple.provenance")
		if len(attr.Xattrs) != 0 {
			t.Fatalf("%s gained attributes from data: %v", rel, attr.Xattrs)
		}
	}

	// Once a target arrives the file is no longer published. The server holds it as attributes unless
	// the client got there first: some kernels remove a ._ file when they create its target.
	testutil.FailErr(t, "write late target", os.WriteFile(filepath.Join(v.mnt, "no-target"), []byte("late"), 0o644))
	v.barrier(t)
	if ch, found := v.changeAt(t, "._no-target"); found {
		t.Fatalf("complete sidecar with a target stayed: %+v", ch)
	}
	if read, err := os.ReadFile(filepath.Join(v.mnt, "._no-target")); err == nil {
		if _, attrs, ok := nfsserver.DecodeAppleDoubleXattrs(read); !ok || string(attrs["user.a"]) != "one" {
			t.Fatalf("held sidecar through the mount: %v ok=%v", attrs, ok)
		}
		if got := v.shell(t, `xattr -p user.a no-target`); strings.TrimSpace(got) != "one" {
			t.Fatalf("attribute after the target arrived: %q", got)
		}
	}

	// Removing data removes data: the target's attributes are not derived from it.
	testutil.FailErr(t, "set attribute", os.WriteFile(filepath.Join(v.mnt, "plain"), []byte("p"), 0o644))
	v.shell(t, `xattr -w user.kept yes plain`)
	testutil.FailErr(t, "write unrelated data", os.WriteFile(filepath.Join(v.mnt, "._target"), []byte("still data"), 0o644))
	testutil.FailErr(t, "remove data", os.Remove(filepath.Join(v.mnt, "._target")))
	if got := v.shell(t, `xattr -p user.kept plain`); strings.TrimSpace(got) != "yes" {
		t.Fatalf("unrelated attribute: %q", got)
	}
}

// TestHeldSidecarsSurviveLeavingTheHandleTable: a walk that pushes held sidecars out of the server's
// memory and a listing that leaves them out keep the attributes on their targets and the ._ files
// out of the change set. A caching client may stop finding the ._ files after the listing, which is
// why the adapter does not claim attribute support.
func TestHeldSidecarsSurviveLeavingTheHandleTable(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{MaxHandles: 16}, map[string]string{"lower.txt": "lower"})
	if v.adp.Facts().MetadataCapabilities.SupportsXattrs {
		t.Fatal("the adapter claims attribute support its caching client cannot keep")
	}
	v.shell(t, `echo new > f; xattr -w user.a v f; xattr -w user.low v lower.txt`)
	v.shell(t, `i=0; while [ $i -lt 64 ]; do echo x > n$i; i=$((i+1)); done; cat n* > /dev/null; ls -a > /dev/null`)
	for target, name := range map[string]string{"f": "user.a", "lower.txt": "user.low"} {
		attr, err := v.core.Lstat(layeredfs.QualifiedPath{RootID: "primary", Rel: target})
		testutil.FailErr(t, "stat "+target, err)
		if string(attr.Xattrs[name]) != "v" {
			t.Fatalf("%s lost %s after eviction: %v", target, name, attr.Xattrs)
		}
	}
	for _, sidecar := range []string{"._f", "._lower.txt"} {
		if ch, found := v.changeAt(t, sidecar); found {
			t.Fatalf("sidecar returned to the change set: %+v", ch)
		}
	}
}
