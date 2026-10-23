package launchverify

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStorePolicyRequiresAnExplicitImmutableClosure(t *testing.T) {
	const store = "/selected/store"
	const output = store + "/generation"
	const image = output + "/lib/code.so"
	policy, err := NewStorePolicy(store, []string{output})
	testutil.FailErr(t, "select closure", err)
	facts := map[string]fileFacts{
		"/": {mode: fs.ModeDir | 0o755}, "/selected": {mode: fs.ModeDir | 0o755},
		store:  {mode: fs.ModeDir | fs.ModeSticky | 0o775},
		output: {mode: fs.ModeDir | 0o555}, output + "/lib": {mode: fs.ModeDir | 0o555},
		image: {inode: 17, mode: 0o555},
	}
	stat := func(path string) (fileFacts, error) {
		value, ok := facts[path]
		if !ok {
			return fileFacts{}, os.ErrNotExist
		}
		return value, nil
	}
	testutil.FailErr(t, "verify selected library", policy.verifyMapping(stat, mapping{17, image}))
	for _, path := range []string{store, store + "/unselected/lib.so", "/usr/lib/libc.so", output + "-other/lib.so", output + "/../generation/lib.so"} {
		if policy.verifyMapping(stat, mapping{17, path}) == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	for _, changed := range []struct {
		path  string
		facts fileFacts
	}{
		{image, fileFacts{inode: 17, mode: 0o755}},
		{image, fileFacts{inode: 18, mode: 0o555}},
		{image, fileFacts{inode: 17, uid: 1000, mode: 0o555}},
		{image, fileFacts{inode: 17, mode: fs.ModeSymlink | 0o555}},
		{output, fileFacts{mode: fs.ModeDir | 0o755}},
		{store, fileFacts{mode: fs.ModeDir | 0o775}},
		{store, fileFacts{mode: fs.ModeDir | fs.ModeSticky | 0o777}},
		{store, fileFacts{uid: 1000, mode: fs.ModeDir | fs.ModeSticky | 0o775}},
		{"/selected", fileFacts{mode: fs.ModeDir | 0o775}},
	} {
		original := facts[changed.path]
		facts[changed.path] = changed.facts
		if policy.verifyMapping(stat, mapping{17, image}) == nil {
			t.Fatalf("accepted modified %s: %+v", changed.path, changed.facts)
		}
		facts[changed.path] = original
	}
	for _, bad := range []struct {
		store   string
		outputs []string
	}{
		{"/", []string{output}}, {"relative", []string{output}}, {store, nil},
		{store, []string{output, output}}, {store, []string{output + "/nested"}},
		{store, []string{"/another/output"}},
	} {
		if _, err := NewStorePolicy(bad.store, bad.outputs); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestSelectedGenerationBindsTheRunningInodeAndManifestRoot(t *testing.T) {
	proc, verifier := newFixture(t)
	proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
	output := filepath.Join(t.TempDir(), "generation")
	root := filepath.Join(output, "lib", "painted-wolf-code")
	image := filepath.Join(root, "bin", "pw")
	testutil.FailErr(t, "create private bin", os.MkdirAll(filepath.Dir(image), 0o755))
	testutil.FailErr(t, "link actual image", os.Link(filepath.Join(proc.root, "images", "301"), image))
	policy, err := NewStorePolicy(filepath.Dir(output), []string{output})
	testutil.FailErr(t, "select generation", err)
	verifier.StorePolicy, verifier.InstallRoot = policy, root
	verifier.Manifest = &pkgmanifest.Manifest{InstallRoot: root, Executables: []pkgmanifest.Executable{entryOf(image, engineImage)}}
	testutil.FailErr(t, "verify actual inode", verifier.VerifyExecutable(301, RoleEngine))
	testutil.FailErr(t, "unlink installed image", os.Remove(image))
	testutil.FailErr(t, "copy identical image", os.WriteFile(image, engineImage, 0o555))
	if err := verifier.VerifyExecutable(301, RoleEngine); CodeOf(err) != CodeExecutableMismatch {
		t.Fatalf("copied inode: %v", err)
	}
	verifier.Manifest.InstallRoot += "-other"
	if err := verifier.VerifyExecutable(301, RoleEngine); CodeOf(err) != CodeManifestUnreadable {
		t.Fatalf("wrong root: %v", err)
	}
	verifier.InstallRoot = ""
	if err := verifier.VerifyExecutable(301, RoleEngine); CodeOf(err) != CodeManifestUnreadable {
		t.Fatalf("unspecified root: %v", err)
	}
}
