package installbinding_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/installbinding"
	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

type install struct {
	t        *testing.T
	root     string
	manifest string
	engine   string
	helper   string
}

func newInstall(t *testing.T) *install {
	t.Helper()
	root := t.TempDir()
	in := &install{t: t, root: root, manifest: filepath.Join(root, "manifest.json"),
		engine: filepath.Join(root, "bin", "pw"), helper: filepath.Join(root, "bin", "pw-document-core")}
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o755); err != nil {
		testutil.FailErr(t, "os.Mkdir failed", err)
	}
	in.write(in.engine, "engine v1")
	in.write(in.helper, "helper v1")
	in.seal(in.engine, in.helper)
	return in
}

func (in *install) write(path, content string) {
	in.t.Helper()
	// A package manager replaces files by rename, so the old inode stays intact.
	staged := path + ".new"
	if err := os.WriteFile(staged, []byte(content), 0o755); err != nil {
		in.t.Fatal(err)
	}
	if err := os.Rename(staged, path); err != nil {
		in.t.Fatal(err)
	}
}

func (in *install) seal(paths ...string) {
	in.t.Helper()
	var executables []pkgmanifest.Executable
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			in.t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		executables = append(executables, pkgmanifest.Executable{Path: path, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw))})
	}
	raw, err := json.Marshal(pkgmanifest.Manifest{FormatVersion: pkgmanifest.FormatVersion, Package: "painted-wolf-code",
		Version: "1.0.2", Architecture: "x86_64", InstallRoot: in.root, Executables: executables})
	if err != nil {
		in.t.Fatal(err)
	}
	in.write(in.manifest, string(raw))
}

func (in *install) capture() *installbinding.Binding {
	in.t.Helper()
	image, err := os.Open(in.engine)
	if err != nil {
		in.t.Fatal(err)
	}
	defer func() { _ = image.Close() }()
	b, err := installbinding.Capture(in.root, in.manifest, in.engine, image)
	if err != nil {
		in.t.Fatal(err)
	}
	return b
}

func refused(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, installbinding.ErrReplaced) {
		t.Fatalf("launch after replacement = %v, want restart outcome", err)
	}
}

func TestUnchangedInstallationAndOutsideProgramsLaunch(t *testing.T) {
	in := newInstall(t)
	b := in.capture()
	for _, path := range []string{in.helper, in.helper, in.engine, "/usr/bin/env", "relative/tool"} {
		if err := b.Verify(path); err != nil {
			t.Fatalf("Verify(%s) = %v", path, err)
		}
	}
}

func TestAReplacedHelperIsRefusedWithARestartOutcome(t *testing.T) {
	in := newInstall(t)
	b := in.capture()
	if err := b.Verify(in.helper); err != nil {
		testutil.FailErr(t, "b.Verify failed", err)
	}
	in.write(in.helper, "helper v2")
	refused(t, b.Verify(in.helper))
}

func TestANewManifestRefusesEvenUnchangedHelpers(t *testing.T) {
	in := newInstall(t)
	b := in.capture()
	in.write(in.engine, "engine v2")
	in.seal(in.engine, in.helper)
	refused(t, b.Verify(in.helper))
}

func TestARewrittenButIdenticalManifestStillLaunches(t *testing.T) {
	in := newInstall(t)
	b := in.capture()
	raw, err := os.ReadFile(in.manifest)
	testutil.FailErr(t, "read file", err)
	in.write(in.manifest, string(raw))
	if err := b.Verify(in.helper); err != nil {
		t.Fatalf("identical reinstall = %v", err)
	}
}

func TestInPlaceChangesAndUnlistedProgramsAreRefused(t *testing.T) {
	in := newInstall(t)
	b := in.capture()
	if err := b.Verify(in.helper); err != nil {
		testutil.FailErr(t, "b.Verify failed", err)
	}
	if err := os.WriteFile(in.helper, []byte("helper vX"), 0o755); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	refused(t, b.Verify(in.helper))
	unlisted := filepath.Join(in.root, "bin", "new-helper")
	in.write(unlisted, "added later")
	refused(t, b.Verify(unlisted))
	refused(t, b.Verify(filepath.Join(in.root, "bin", "missing")))
}

func TestLinksResolveToTheListedProgram(t *testing.T) {
	in := newInstall(t)
	b := in.capture()
	link := filepath.Join(in.root, "bin", "core")
	if err := os.Symlink("pw-document-core", link); err != nil {
		testutil.FailErr(t, "os.Symlink failed", err)
	}
	if err := b.Verify(link); err != nil {
		t.Fatalf("link to listed helper = %v", err)
	}
	other := filepath.Join(in.root, "other")
	in.write(other, "unlisted")
	escape := filepath.Join(in.root, "bin", "escape")
	if err := os.Symlink("../other", escape); err != nil {
		testutil.FailErr(t, "os.Symlink failed", err)
	}
	refused(t, b.Verify(escape))
}

func TestCaptureRequiresTheRunningImageToBeTheListedEngine(t *testing.T) {
	in := newInstall(t)
	image, err := os.Open(in.engine)
	testutil.FailErr(t, "os.Open failed", err)
	defer func() { _ = image.Close() }()
	in.write(in.engine, "engine v2")
	in.seal(in.engine, in.helper)
	// The manifest now describes a replacement; the opened image is the old engine.
	if _, err := installbinding.Capture(in.root, in.manifest, in.engine, image); !errors.Is(err, installbinding.ErrReplaced) {
		t.Fatalf("capture of a replaced engine = %v", err)
	}
	if _, err := installbinding.Capture(in.root, in.manifest, filepath.Join(in.root, "bin", "absent"), image); err == nil {
		t.Fatal("captured an engine the manifest does not list")
	}
	if _, err := installbinding.Capture(in.root, filepath.Join(in.root, "none.json"), in.engine, image); err == nil {
		t.Fatal("captured without a manifest")
	}
}

func TestTheProcessBindingAppliesToEveryLaunch(t *testing.T) {
	in := newInstall(t)
	installbinding.Install(in.capture())
	t.Cleanup(func() { installbinding.Install(nil) })
	in.write(in.helper, "helper v2")
	refused(t, installbinding.Verify(in.helper))
	installbinding.Install(nil)
	if err := installbinding.Verify(in.helper); err != nil {
		t.Fatalf("unbound process refused a launch: %v", err)
	}
}
