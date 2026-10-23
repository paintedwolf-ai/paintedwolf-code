package launchverify

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

func selectedChainFixture(t *testing.T) (*fakeProc, Verifier) {
	t.Helper()
	proc, verifier := newFixture(t)
	root := filepath.Join(proc.generationsDir, proc.activeGeneration)
	manifest := *verifier.Manifest
	manifest.InstallRoot = root
	manifest.Executables = append([]pkgmanifest.Executable(nil), manifest.Executables...)
	for index := range manifest.Executables {
		relative, err := filepath.Rel(DefaultInstallRoot, manifest.Executables[index].Path)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Executables[index].Path = filepath.Join(root, relative)
	}
	policy, err := NewStorePolicy(proc.generationsDir, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	verifier.Manifest, verifier.InstallRoot, verifier.StorePolicy = &manifest, root, policy
	proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, start: 10, image: desktopImage})
	proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, start: 20, image: engineImage})
	for _, pid := range []int{300, 301} {
		// Closure mapping rules have separate positive/negative map fixtures. This
		// chain fixture isolates exact selected images and namespace authority.
		if err := os.WriteFile(filepath.Join(proc.root, strconv.Itoa(pid), "maps"), []byte("7fff00000000-7fff00002000 r-xp 00000000 00:00 0 [vdso]\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return proc, verifier
}

func TestSelectedClosureChainRetainsNamespaceAndRecheckAuthority(t *testing.T) {
	proc, verifier := selectedChainFixture(t)
	chain, err := verifier.VerifyChain(301, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if chain.Manifest != verifier.Manifest || chain.Generation != "" {
		t.Fatal("explicit closure was reinterpreted as native generation selection")
	}
	status := filepath.Join(proc.root, "301", "status")
	raw, err := os.ReadFile(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(status, []byte(strings.Replace(string(raw), "TracerPid:\t0", "TracerPid:\t1", 1)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifier.Recheck(chain); CodeOf(err) != CodeProcessTraced {
		t.Fatalf("traced explicit closure=%v", err)
	}
}

func TestSelectedClosureCannotBypassTheProtectedNamespaceOrExactRoot(t *testing.T) {
	_, verifier := selectedChainFixture(t)
	original := verifier.NamespaceReader
	verifier.NamespaceReader = func(root string, pid int) (NamespaceFacts, error) { return original(root, 0) }
	if _, err := verifier.VerifyChain(301, 1000); CodeOf(err) != CodeNamespaceInvalid {
		t.Fatalf("unprotected explicit closure=%v", err)
	}
	verifier.NamespaceReader = original
	root := verifier.InstallRoot
	for _, invalid := range []string{"", root + "/../" + filepath.Base(root), root + "-other"} {
		verifier.InstallRoot = invalid
		if _, err := verifier.VerifyChain(301, 1000); CodeOf(err) != CodeManifestUnreadable {
			t.Fatalf("invalid selected root %q=%v", invalid, err)
		}
	}
}
