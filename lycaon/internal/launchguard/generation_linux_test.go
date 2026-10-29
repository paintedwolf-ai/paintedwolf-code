//go:build linux

package launchguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestGenerationPolicyRejectsUnboundSelection(t *testing.T) {
	valid := `{"version":1,"generation_root":"/opt/painted-wolf-code/generations/abc","manifest_sha256":"` + strings.Repeat("a", 64) + `","logical_install_root":"/opt/painted-wolf-code","library_roots":["/usr/lib"]}`
	if _, err := decodePolicy([]byte(valid)); err != nil {
		t.Fatalf("decode bound generation: %v", err)
	}
	cases := map[string]string{
		"unknown field":       strings.TrimSuffix(valid, "}") + `,"command":"/bin/sh"}`,
		"trailing document":   valid + `{}`,
		"relative generation": strings.Replace(valid, `"/opt/painted-wolf-code/generations/abc"`, `"relative"`, 1),
		"unclean generation":  strings.Replace(valid, `generations/abc`, `generations/../abc`, 1),
		"root generation":     strings.Replace(valid, `"/opt/painted-wolf-code/generations/abc"`, `"/"`, 1),
		"uppercase hash":      strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"short hash":          strings.Replace(valid, strings.Repeat("a", 64), "ab", 1),
		"future version":      strings.Replace(valid, `"version":1`, `"version":2`, 1),
		"relative executable": strings.TrimSuffix(valid, "}") + `,"system_executables":["usr/libexec"]}`,
		"root executable":     strings.TrimSuffix(valid, "}") + `,"system_executables":["/"]}`,
		"no library roots":    strings.Replace(valid, `,"library_roots":["/usr/lib"]`, "", 1),
		"empty library roots": strings.Replace(valid, `["/usr/lib"]`, `[]`, 1),
		"relative library":    strings.Replace(valid, `["/usr/lib"]`, `["usr/lib"]`, 1),
		"root library":        strings.Replace(valid, `["/usr/lib"]`, `["/"]`, 1),
		"unclean library":     strings.Replace(valid, `["/usr/lib"]`, `["/usr/../home"]`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodePolicy([]byte(raw)); err == nil {
				t.Fatal("accepted unbound generation")
			}
		})
	}
}

func withTestTrustAnchor(t *testing.T, base string) {
	t.Helper()
	prevAnchor, prevOwner := trustAnchor, trustedOwner
	prevPolicy, prevLocks := policyPath, lockDirectory
	trustAnchor = base
	trustedOwner = uint32(os.Geteuid())
	t.Cleanup(func() {
		trustAnchor = prevAnchor
		trustedOwner = prevOwner
		policyPath = prevPolicy
		lockDirectory = prevLocks
	})
}

func TestTrustedPathRejectsRelativeWritableStickyAndSymlinkAncestors(t *testing.T) {
	if file, err := openRootControlled("relative", false); err == nil {
		_ = file.Close()
		t.Fatal("accepted relative authority")
	}
	// Sticky world-writable directories such as /tmp are never exempted.
	stickyFile := filepath.Join("/tmp", "pw-sticky-probe-"+filepath.Base(t.TempDir()))
	if file, err := openRootControlled(stickyFile, false); err == nil {
		_ = file.Close()
		t.Fatal("accepted path under sticky /tmp")
	}
	// A caller-owned temporary directory is not trusted when anchored at "/".
	if file, err := openRootControlled(t.TempDir()+"/missing", false); err == nil {
		_ = file.Close()
		t.Fatal("accepted caller-owned authority under default anchor")
	}

	base := filepath.Join(t.TempDir(), "anchor")
	testutil.FailErr(t, "mkdir anchor", os.MkdirAll(filepath.Join(base, "sub"), 0o755))
	testutil.FailErr(t, "chmod anchor", os.Chmod(base, 0o755))
	testutil.FailErr(t, "chmod sub", os.Chmod(filepath.Join(base, "sub"), 0o755))
	regular := filepath.Join(base, "sub", "file")
	testutil.FailErr(t, "write file", os.WriteFile(regular, []byte("ok"), 0o644))
	testutil.FailErr(t, "chmod file", os.Chmod(regular, 0o644))

	withTestTrustAnchor(t, base)

	dirFile, err := openRootControlled(base, true)
	testutil.FailErr(t, "open anchor directory", err)
	_ = dirFile.Close()
	if f, err := openRootControlled(base, false); err == nil {
		_ = f.Close()
		t.Fatal("opened anchor directory as regular file")
	}
	regFile, err := openRootControlled(regular, false)
	testutil.FailErr(t, "open controlled regular file", err)
	_ = regFile.Close()
	if f, err := openRootControlled(regular, true); err == nil {
		_ = f.Close()
		t.Fatal("opened regular file as directory")
	}

	// Symlink leaf and symlink intermediate component must be refused.
	linkLeaf := filepath.Join(base, "sub", "link")
	testutil.FailErr(t, "symlink leaf", os.Symlink(regular, linkLeaf))
	if f, err := openRootControlled(linkLeaf, false); err == nil {
		_ = f.Close()
		t.Fatal("followed symlink leaf")
	}
	linkDir := filepath.Join(base, "linkdir")
	testutil.FailErr(t, "symlink dir", os.Symlink(filepath.Join(base, "sub"), linkDir))
	if f, err := openRootControlled(filepath.Join(linkDir, "file"), false); err == nil {
		_ = f.Close()
		t.Fatal("followed symlink directory")
	}

	// Sticky or group-writable ancestor must be refused.
	testutil.FailErr(t, "sticky sub", os.Chmod(filepath.Join(base, "sub"), 0o755|os.ModeSticky))
	if f, err := openRootControlled(regular, false); err == nil {
		_ = f.Close()
		t.Fatal("accepted sticky ancestor")
	}
	testutil.FailErr(t, "group-writable sub", os.Chmod(filepath.Join(base, "sub"), 0o775))
	if f, err := openRootControlled(regular, false); err == nil {
		_ = f.Close()
		t.Fatal("accepted group-writable ancestor")
	}
	testutil.FailErr(t, "restore sub", os.Chmod(filepath.Join(base, "sub"), 0o755))

	// Foreign owner must be refused.
	trustedOwner = uint32(os.Geteuid() + 1)
	if f, err := openRootControlled(regular, false); err == nil {
		_ = f.Close()
		t.Fatal("accepted foreign owner")
	}
}

func TestCaptureGenerationAndPolicyRecheck(t *testing.T) {
	base := filepath.Join(t.TempDir(), "anchor")
	install := filepath.Join(base, "install")
	staging := filepath.Join(base, "staging")
	for _, dir := range []string{base, install, filepath.Join(install, "bin"), staging, filepath.Join(staging, "bin")} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o755))
		testutil.FailErr(t, "chmod "+dir, os.Chmod(dir, 0o755))
	}
	withTestTrustAnchor(t, base)
	policyPath = filepath.Join(base, "launch.json")
	lockDirectory = filepath.Join(base, "locks")

	var executables []pkgmanifest.Executable
	var payload []pkgmanifest.PayloadEntry
	payload = append(payload, pkgmanifest.PayloadEntry{Path: "bin", Kind: "directory", Mode: 0o755})
	for _, name := range []string{"pw-launch-entry", "painted-wolf-code"} {
		content := []byte("#!/bin/sh\n")
		target := filepath.Join(staging, "bin", name)
		testutil.FailErr(t, "write "+name, os.WriteFile(target, content, 0o755))
		testutil.FailErr(t, "chmod "+name, os.Chmod(target, 0o755))
		sum := sha256.Sum256(content)
		digest := hex.EncodeToString(sum[:])
		size := int64(len(content))
		executables = append(executables, pkgmanifest.Executable{Path: filepath.Join(install, "bin", name), SHA256: digest, Size: size})
		payload = append(payload, pkgmanifest.PayloadEntry{Path: "bin/" + name, Kind: "file", Mode: 0o755, SHA256: &digest, Size: &size})
	}
	arch, _ := platformfloor.ELFArchForGoArch(runtime.GOARCH)
	manifest := pkgmanifest.Manifest{
		FormatVersion: pkgmanifest.FormatVersion,
		Package:       "painted-wolf-code",
		Version:       "1.0.0",
		Architecture:  arch,
		InstallRoot:   install,
		Executables:   executables,
		Payload:       payload,
	}
	rawManifest, err := json.Marshal(manifest)
	testutil.FailErr(t, "marshal manifest", err)
	testutil.FailErr(t, "write manifest", os.WriteFile(filepath.Join(staging, "manifest.json"), rawManifest, 0o644))
	testutil.FailErr(t, "chmod manifest", os.Chmod(filepath.Join(staging, "manifest.json"), 0o644))
	sum := sha256.Sum256(rawManifest)
	digest := hex.EncodeToString(sum[:])
	genDir := filepath.Join(base, "generations", digest)
	testutil.FailErr(t, "mkdir generations", os.MkdirAll(filepath.Join(base, "generations"), 0o755))
	testutil.FailErr(t, "chmod generations", os.Chmod(filepath.Join(base, "generations"), 0o755))
	testutil.FailErr(t, "rename generation", os.Rename(staging, genDir))

	pol := GenerationPolicy{Version: 1, GenerationRoot: genDir, ManifestSHA256: digest, LogicalInstallRoot: install, LibraryRoots: []string{base}}
	if err := WritePolicy(GenerationPolicy{Version: 2}); err == nil {
		t.Fatal("WritePolicy accepted invalid policy")
	}
	testutil.FailErr(t, "WritePolicy initial", WritePolicy(pol))
	testutil.FailErr(t, "WritePolicy replace", WritePolicy(pol))

	gen, err := CaptureGeneration()
	testutil.FailErr(t, "CaptureGeneration", err)
	gen.Close()

	if !samePolicy(pol, gen.Policy) {
		t.Fatalf("captured policy %+v != %+v", gen.Policy, pol)
	}
	modified := pol
	modified.SystemExecutables = []string{"/usr/libexec"}
	if samePolicy(pol, modified) {
		t.Fatal("samePolicy ignored SystemExecutables change")
	}

	// Policy mismatching manifest hash is refused.
	badDigest := pol
	badDigest.ManifestSHA256 = strings.Repeat("b", 64)
	testutil.FailErr(t, "WritePolicy badDigest", WritePolicy(badDigest))
	if g, err := CaptureGeneration(); err == nil {
		g.Close()
		t.Fatal("CaptureGeneration accepted mismatched manifest digest")
	}

	// Policy mismatching logical install root is refused.
	badRoot := pol
	badRoot.LogicalInstallRoot = filepath.Join(base, "other")
	testutil.FailErr(t, "WritePolicy badRoot", WritePolicy(badRoot))
	if g, err := CaptureGeneration(); err == nil {
		g.Close()
		t.Fatal("CaptureGeneration accepted mismatched logical install root")
	}

	// Exclusively locked generation lock is refused as reclaiming.
	testutil.FailErr(t, "WritePolicy restore", WritePolicy(pol))
	lockFile, err := os.OpenFile(filepath.Join(lockDirectory, digest+".lock"), os.O_RDWR, 0)
	testutil.FailErr(t, "open lock file", err)
	testutil.FailErr(t, "flock EX", unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB))
	if err := WritePolicy(pol); err == nil {
		_ = lockFile.Close()
		t.Fatal("WritePolicy selected a generation whose lock is exclusively held")
	}
	if g, err := CaptureGeneration(); err == nil {
		g.Close()
		_ = lockFile.Close()
		t.Fatal("CaptureGeneration succeeded while generation lock was exclusively held")
	}
	_ = lockFile.Close()

	// Corrupted desktop binary in generation is refused by VerifyOpened.
	desktopBin := filepath.Join(genDir, "bin/painted-wolf-code")
	testutil.FailErr(t, "corrupt desktop binary", os.WriteFile(desktopBin, []byte("#!/bin/sh\necho bad\n"), 0o755))
	if g, err := CaptureGeneration(); err == nil {
		g.Close()
		t.Fatal("CaptureGeneration accepted corrupted desktop binary")
	}

	// A policy whose parent is not root-controlled, or whose current file is
	// not a regular file, is never replaced.
	selected := policyPath
	policyPath = filepath.Join(base, "missing", "policy.json")
	if err := WritePolicy(pol); err == nil {
		t.Fatal("WritePolicy wrote below a missing policy directory")
	}
	policyPath = selected
	testutil.FailErr(t, "remove policy", os.Remove(policyPath))
	testutil.FailErr(t, "policy directory", os.Mkdir(policyPath, 0o755))
	if err := WritePolicy(pol); err == nil {
		t.Fatal("WritePolicy replaced a policy that is not a regular file")
	}
	testutil.FailErr(t, "remove policy directory", os.Remove(policyPath))

	// Oversized policy file is refused.
	testutil.FailErr(t, "write oversized policy", os.WriteFile(policyPath, []byte(strings.Repeat(" ", 16385)), 0o644))
	testutil.FailErr(t, "chmod oversized policy", os.Chmod(policyPath, 0o644))
	if _, err := ReadPolicy(); err == nil {
		t.Fatal("ReadPolicy accepted oversized policy")
	}
}

func ownedPrivateDirectory(info os.FileInfo) error {
	facts, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(facts.Uid) != os.Geteuid() || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return errors.New("not a private owned directory")
	}
	return nil
}

func TestLockDirectoryIsCreatedUnderControlledAncestry(t *testing.T) {
	base := t.TempDir()
	testutil.FailErr(t, "seal base", os.Chmod(base, 0o755))
	handle, err := os.Open(base)
	testutil.FailErr(t, "open base", err)
	defer handle.Close()
	syscall.Umask(0o002)
	defer syscall.Umask(0o022)
	// The directory is absent after boot; the first launch creates it.
	dir, err := ensureControlledDirectory(handle, "/run/paintedwolf/generation-locks", ownedPrivateDirectory)
	testutil.FailErr(t, "create lock directory", err)
	_ = dir.Close()
	for _, rel := range []string{"run", "run/paintedwolf", "run/paintedwolf/generation-locks"} {
		info, err := os.Lstat(filepath.Join(base, rel))
		testutil.FailErr(t, "stat "+rel, err)
		if !info.IsDir() || info.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode %v, want directory 0755 despite umask", rel, info.Mode())
		}
	}
	// An existing directory is reused.
	again, err := ensureControlledDirectory(handle, "/run/paintedwolf/generation-locks", ownedPrivateDirectory)
	testutil.FailErr(t, "reuse lock directory", err)
	_ = again.Close()
	// A writable or replaced component is refused, never followed.
	testutil.FailErr(t, "loosen ancestor", os.Chmod(filepath.Join(base, "run/paintedwolf"), 0o777))
	if dir, err := ensureControlledDirectory(handle, "/run/paintedwolf/generation-locks", ownedPrivateDirectory); err == nil {
		_ = dir.Close()
		t.Fatal("accepted a writable ancestor")
	}
	testutil.FailErr(t, "restore ancestor", os.Chmod(filepath.Join(base, "run/paintedwolf"), 0o755))
	// A component that cannot be created is refused.
	testutil.FailErr(t, "plain file component", os.WriteFile(filepath.Join(base, "run/plain"), nil, 0o644))
	if dir, err := ensureControlledDirectory(handle, "/run/plain/generation-locks", ownedPrivateDirectory); err == nil {
		_ = dir.Close()
		t.Fatal("created a directory below a regular file")
	}
	// A missing trust anchor is refused.
	if file, err := openAnchor(filepath.Join(base, "absent")); err == nil {
		_ = file.Close()
		t.Fatal("opened a missing trust anchor")
	}
	testutil.FailErr(t, "remove lock directory", os.Remove(filepath.Join(base, "run/paintedwolf/generation-locks")))
	testutil.FailErr(t, "plant link", os.Symlink(t.TempDir(), filepath.Join(base, "run/paintedwolf/generation-locks")))
	if dir, err := ensureControlledDirectory(handle, "/run/paintedwolf/generation-locks", ownedPrivateDirectory); err == nil {
		_ = dir.Close()
		t.Fatal("followed a planted link")
	}
	for _, path := range []string{"relative", "/run/../etc", "/"} {
		if dir, err := ensureControlledDirectory(handle, path, ownedPrivateDirectory); err == nil {
			_ = dir.Close()
			t.Fatalf("accepted %q", path)
		}
	}
}
