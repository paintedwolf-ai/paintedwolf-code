package launchverify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/testutil"
)

// fakeProc builds a synthetic process filesystem and package generation store.
type fakeProc struct {
	t                *testing.T
	base             string
	root             string
	generationsDir   string
	policyPath       string
	locksDir         string
	activeGeneration string
	files            map[string]fileFacts
}

func (f *fakeProc) stat(path string) (fileFacts, error) {
	if facts, ok := f.files[path]; ok {
		return facts, nil
	}
	return fileFacts{}, os.ErrNotExist
}

type fakeProcess struct {
	pid, ppid, uid, euid int
	start                uint64
	image                []byte
	generation           string
	exePath              string
}

func (f *fakeProc) add(p fakeProcess) {
	dir := filepath.Join(f.root, strconv.Itoa(p.pid))
	testutil.FailErr(f.t, "create proc dir", os.MkdirAll(dir, 0o755))
	stat := fmt.Sprintf("%d (pw (sidecar)) S %d %d %d 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 %d 1 1 1", p.pid, p.ppid, p.pid, p.pid, p.start)
	testutil.FailErr(f.t, "write stat", os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644))
	status := fmt.Sprintf("Name:\tpw\nTracerPid:\t0\nPPid:\t%d\nUid:\t%d\t%d\t%d\t%d\nGid:\t1000\t1000\t1000\t1000\n", p.ppid, p.uid, p.euid, p.uid, p.euid)
	testutil.FailErr(f.t, "write status", os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644))
	target := p.exePath
	gen := p.generation
	if gen == "" {
		gen = f.activeGeneration
	}
	if target == "" && gen != "" {
		switch {
		case bytes.Equal(p.image, engineImage):
			target = filepath.Join(f.generationsDir, gen, "bin/pw")
		case bytes.Equal(p.image, desktopImage):
			target = filepath.Join(f.generationsDir, gen, "bin/painted-wolf-code")
		}
	}
	if target == "" {
		target = filepath.Join(f.root, "images", strconv.Itoa(p.pid))
		testutil.FailErr(f.t, "create image dir", os.MkdirAll(filepath.Dir(target), 0o755))
		testutil.FailErr(f.t, "write image", os.WriteFile(target, p.image, 0o755))
	}
	testutil.FailErr(f.t, "link exe", os.Symlink(target, filepath.Join(dir, "exe")))
	installed := fmt.Sprintf("/opt/painted-wolf-code/image-%d", p.pid)
	f.files[installed] = fileFacts{device: 0xfd01, inode: uint64(p.pid), mode: 0o755}
	maps := fmt.Sprintf("00400000-00500000 r-xp 00000000 fd:01 %d %s\n"+
		"00600000-00700000 rw-p 00000000 00:00 0 \n"+
		"7f0000000000-7f0000001000 r-xp 00000000 00:00 0 \n"+
		"7fff00000000-7fff00002000 r-xp 00000000 00:00 0                          [vdso]\n", p.pid, installed)
	testutil.FailErr(f.t, "write maps", os.WriteFile(filepath.Join(dir, "maps"), []byte(maps), 0o644))
}

func (f *fakeProc) appendMaps(pid int, lines string) {
	path := filepath.Join(f.root, strconv.Itoa(pid), "maps")
	raw, err := os.ReadFile(path)
	testutil.FailErr(f.t, "read maps", err)
	testutil.FailErr(f.t, "append maps", os.WriteFile(path, append(raw, lines...), 0o644))
}

func (f *fakeProc) stageGeneration(version string, desktop, engine []byte, extra ...pkgmanifest.Executable) (string, *pkgmanifest.Manifest) {
	f.t.Helper()
	executables := append([]pkgmanifest.Executable{
		entryOf(EnginePath, engine),
		entryOf(DesktopAppPath, desktop),
	}, extra...)
	payloadMap := map[string]pkgmanifest.PayloadEntry{
		"bin": {Path: "bin", Kind: "directory", Mode: 0o755},
	}
	for _, exe := range executables {
		rel := strings.TrimPrefix(exe.Path, DefaultInstallRoot+"/")
		for parent := filepath.Dir(rel); parent != "."; parent = filepath.Dir(parent) {
			payloadMap[parent] = pkgmanifest.PayloadEntry{Path: parent, Kind: "directory", Mode: 0o755}
		}
		sum, size := exe.SHA256, exe.Size
		payloadMap[rel] = pkgmanifest.PayloadEntry{Path: rel, Kind: "file", Mode: 0o755, SHA256: &sum, Size: &size}
	}
	payload := make([]pkgmanifest.PayloadEntry, 0, len(payloadMap))
	for _, row := range payloadMap {
		payload = append(payload, row)
	}
	sort.Slice(payload, func(i, j int) bool { return payload[i].Path < payload[j].Path })
	arch, ok := platformfloor.ELFArchForGoArch(runtime.GOARCH)
	if !ok {
		arch = "x86_64"
	}
	manifest := &pkgmanifest.Manifest{
		FormatVersion: pkgmanifest.FormatVersion,
		Package:       "painted-wolf-code",
		Version:       version,
		Architecture:  arch,
		InstallRoot:   DefaultInstallRoot,
		Executables:   executables,
		Payload:       payload,
	}
	raw, err := json.Marshal(manifest)
	testutil.FailErr(f.t, "marshal manifest", err)
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	genDir := filepath.Join(f.generationsDir, sha)
	testutil.FailErr(f.t, "mkdir generation bin", os.MkdirAll(filepath.Join(genDir, "bin"), 0o755))
	testutil.FailErr(f.t, "chmod generations dir", os.Chmod(f.generationsDir, 0o755))
	testutil.FailErr(f.t, "chmod generation dir", os.Chmod(genDir, 0o755))
	testutil.FailErr(f.t, "chmod generation bin", os.Chmod(filepath.Join(genDir, "bin"), 0o755))
	testutil.FailErr(f.t, "write engine", os.WriteFile(filepath.Join(genDir, "bin/pw"), engine, 0o755))
	testutil.FailErr(f.t, "chmod engine", os.Chmod(filepath.Join(genDir, "bin/pw"), 0o755))
	testutil.FailErr(f.t, "write desktop", os.WriteFile(filepath.Join(genDir, "bin/painted-wolf-code"), desktop, 0o755))
	testutil.FailErr(f.t, "chmod desktop", os.Chmod(filepath.Join(genDir, "bin/painted-wolf-code"), 0o755))
	testutil.FailErr(f.t, "write manifest", os.WriteFile(filepath.Join(genDir, "manifest.json"), raw, 0o644))
	testutil.FailErr(f.t, "chmod manifest", os.Chmod(filepath.Join(genDir, "manifest.json"), 0o644))
	return sha, manifest
}

func (f *fakeProc) activateGeneration(sha string) {
	f.t.Helper()
	policy := generationPolicy{
		Version:            1,
		GenerationRoot:     filepath.Join(f.generationsDir, sha),
		ManifestSHA256:     sha,
		LogicalInstallRoot: DefaultInstallRoot,
	}
	raw, err := json.Marshal(policy)
	testutil.FailErr(f.t, "marshal policy", err)
	testutil.FailErr(f.t, "write policy", os.WriteFile(f.policyPath, raw, 0o644))
	testutil.FailErr(f.t, "chmod policy", os.Chmod(f.policyPath, 0o644))
	f.activeGeneration = sha
}

func (f *fakeProc) lockGeneration(sha string) *os.File {
	f.t.Helper()
	testutil.FailErr(f.t, "mkdir locks", os.MkdirAll(f.locksDir, 0o755))
	testutil.FailErr(f.t, "chmod locks", os.Chmod(f.locksDir, 0o755))
	lockPath := filepath.Join(f.locksDir, sha+".lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	testutil.FailErr(f.t, "open generation lock", err)
	testutil.FailErr(f.t, "chmod generation lock", os.Chmod(lockPath, 0o600))
	testutil.FailErr(f.t, "flock generation SH", syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB))
	f.t.Cleanup(func() { _ = file.Close() })
	return file
}

func entryOf(path string, image []byte) pkgmanifest.Executable {
	sum := sha256.Sum256(image)
	return pkgmanifest.Executable{Path: path, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(image))}
}

var (
	engineImage  = []byte("engine image bytes")
	desktopImage = []byte("desktop app image bytes")
)

func newFixture(t *testing.T) (*fakeProc, Verifier) {
	t.Helper()
	base := t.TempDir()
	testutil.FailErr(t, "chmod base", os.Chmod(base, 0o755))
	procRoot := filepath.Join(base, "proc")
	testutil.FailErr(t, "mkdir proc", os.MkdirAll(procRoot, 0o755))
	proc := &fakeProc{
		t:              t,
		base:           base,
		root:           procRoot,
		generationsDir: filepath.Join(base, "generations"),
		policyPath:     filepath.Join(base, "launch.json"),
		locksDir:       filepath.Join(base, "locks"),
		files: map[string]fileFacts{
			"/":                      {mode: fs.ModeDir | 0o755},
			"/opt":                   {mode: fs.ModeDir | 0o755},
			"/opt/painted-wolf-code": {mode: fs.ModeDir | 0o755},
		},
	}
	sha, manifest := proc.stageGeneration("1.0.0", desktopImage, engineImage, entryOf("/opt/painted-wolf-code/bin/pw-logs", []byte("logs")))
	proc.activateGeneration(sha)
	return proc, Verifier{
		ProcRoot:        procRoot,
		Manifest:        manifest,
		GenerationsDir:  proc.generationsDir,
		PolicyPath:      proc.policyPath,
		LocksDir:        proc.locksDir,
		TrustAnchor:     base,
		TrustedOwner:    uint32(os.Geteuid()),
		statFile:        proc.stat,
		NamespaceReader: fixtureNamespace,
	}
}

func TestVerifyChainAcceptsTheInstalledEngineUnderTheInstalledApp(t *testing.T) {
	proc, verifier := newFixture(t)
	proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, start: 10, image: desktopImage})
	proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, start: 20, image: engineImage})
	chain, err := verifier.VerifyChain(301, 1000)
	testutil.FailErr(t, "verify chain", err)
	if chain.Engine.PID != 301 || chain.Launcher.PID != 300 || chain.Engine.StartTime != 20 || chain.Generation != proc.activeGeneration {
		t.Fatalf("chain = %+v", chain)
	}
	testutil.FailErr(t, "recheck chain", verifier.Recheck(chain))
}

func TestVerifyChainRefusesWithStructuredCodes(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(*fakeProc)
		uid   int
		code  Code
		role  string
	}{
		{name: "unknown pid", setup: func(*fakeProc) {}, uid: 1000, code: CodeProcessGone, role: RoleEngine},
		{name: "another user's engine", uid: 1001, code: CodeUIDMismatch, role: RoleEngine, setup: func(p *fakeProc) {
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: desktopImage})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "setuid engine", uid: 1000, code: CodeUIDMismatch, role: RoleEngine, setup: func(p *fakeProc) {
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 0, image: engineImage})
		}},
		{name: "orphaned engine", uid: 1000, code: CodeNoParent, role: RoleEngine, setup: func(p *fakeProc) {
			p.add(fakeProcess{pid: 301, ppid: 1, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "parent exited", uid: 1000, code: CodeProcessGone, role: RoleDesktopApp, setup: func(p *fakeProc) {
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "parent owned by another user", uid: 1000, code: CodeUIDMismatch, role: RoleDesktopApp, setup: func(p *fakeProc) {
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 0, euid: 0, image: desktopImage})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "modified engine", uid: 1000, code: CodeExecutableMismatch, role: RoleEngine, setup: func(p *fakeProc) {
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: desktopImage})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: []byte("patched engine")})
		}},
		{name: "shell is not the desktop app", uid: 1000, code: CodeExecutableMismatch, role: RoleDesktopApp, setup: func(p *fakeProc) {
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: []byte("bash")})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "another installed executable as the parent", uid: 1000, code: CodeExecutableMismatch, role: RoleDesktopApp, setup: func(p *fakeProc) {
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: []byte("logs")})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "unreadable engine image", uid: 1000, code: CodeExecutableUnreadable, role: RoleEngine, setup: func(p *fakeProc) {
			missing := filepath.Join(p.root, "images", "missing-301")
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: desktopImage})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage, exePath: missing})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			proc, verifier := newFixture(t)
			tt.setup(proc)
			_, err := verifier.VerifyChain(301, tt.uid)
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if refusal.Code != tt.code || refusal.Role != tt.role || CodeOf(err) != tt.code {
				t.Fatalf("refusal = %+v, want %s for %s", refusal, tt.code, tt.role)
			}
		})
	}
}

func TestVerifyChainNeedsTheManifestRoles(t *testing.T) {
	proc, verifier := newFixture(t)
	proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: desktopImage})
	proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
	// The desktop app listed under its name rather than its installed path.
	verifier.Manifest = &pkgmanifest.Manifest{Executables: []pkgmanifest.Executable{
		entryOf(EnginePath, engineImage), entryOf("painted-wolf-code", desktopImage),
	}}
	if err := verifier.VerifyExecutable(300, RoleDesktopApp); CodeOf(err) != CodeManifestUnreadable {
		t.Fatalf("err = %v", err)
	}
	verifier.Manifest = nil
	if err := verifier.VerifyExecutable(301, RoleEngine); CodeOf(err) != CodeManifestUnreadable {
		t.Fatalf("no manifest: %v", err)
	}
	// Equal digests with a different recorded size are not the installed file.
	shorter := entryOf(EnginePath, engineImage)
	shorter.Size--
	verifier.Manifest = &pkgmanifest.Manifest{Executables: []pkgmanifest.Executable{shorter, entryOf(DesktopAppPath, desktopImage)}}
	if err := verifier.VerifyExecutable(301, RoleEngine); CodeOf(err) != CodeExecutableMismatch {
		t.Fatalf("size mismatch: %v", err)
	}
	// Missing launch policy makes the generation manifest unreadable on VerifyChain.
	testutil.FailErr(t, "remove policy", os.Remove(proc.policyPath))
	if _, err := verifier.VerifyChain(301, 1000); CodeOf(err) != CodeManifestUnreadable {
		t.Fatalf("missing policy: %v", err)
	}
	if CodeOf(errors.New("plain")) != "" {
		t.Fatal("a plain error carries a code")
	}
}

func TestRecheckRefusesReusedPIDsAndReparenting(t *testing.T) {
	proc, verifier := newFixture(t)
	proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, start: 10, image: desktopImage})
	proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, start: 20, image: engineImage})
	chain, err := verifier.VerifyChain(301, 1000)
	testutil.FailErr(t, "verify chain", err)

	testutil.FailErr(t, "remove engine", os.RemoveAll(filepath.Join(proc.root, "301")))
	proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, start: 99, image: engineImage})
	if err := verifier.Recheck(chain); CodeOf(err) != CodeProcessChanged {
		t.Fatalf("reused pid: %v", err)
	}

	testutil.FailErr(t, "remove engine again", os.RemoveAll(filepath.Join(proc.root, "301")))
	proc.add(fakeProcess{pid: 301, ppid: 1, uid: 1000, euid: 1000, start: 20, image: engineImage})
	if err := verifier.Recheck(chain); CodeOf(err) != CodeProcessChanged {
		t.Fatalf("reparented engine: %v", err)
	}

	testutil.FailErr(t, "restore engine", os.RemoveAll(filepath.Join(proc.root, "301")))
	proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, start: 20, image: engineImage})
	testutil.FailErr(t, "remove launcher", os.RemoveAll(filepath.Join(proc.root, "300")))
	if err := verifier.Recheck(chain); CodeOf(err) != CodeProcessGone {
		t.Fatalf("exited launcher: %v", err)
	}
}

func TestSnapshotReadsStatThroughConfiguredProcRoot(t *testing.T) {
	proc, verifier := newFixture(t)
	proc.add(fakeProcess{pid: 42, ppid: 7, uid: 1000, euid: 1001, start: 12345})
	path := verifier.procPath(42, "stat")
	testutil.FailErr(t, "write command stat", os.WriteFile(path, []byte("42 (a (b) c) S 7 42 42 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 12345 1 1 1"), 0o644))
	snap, err := verifier.snapshot(42)
	testutil.FailErr(t, "snapshot stat", err)
	if snap.PID != 42 || snap.PPID != 7 || snap.StartTime != 12345 || snap.UIDs != [4]uint32{1000, 1001, 1000, 1001} {
		t.Fatalf("snapshot = %+v", snap)
	}
	testutil.FailErr(t, "write mismatched pid", os.WriteFile(path, []byte("43 (pw) S 7 42 42 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 12345"), 0o644))
	if _, err := verifier.snapshot(42); err == nil {
		t.Fatal("accepted another process record")
	}
	for _, bad := range []string{"", "42 S 7", "42 (x) S 7 1", "x (x) S 7 42 42 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 1 1 1 1"} {
		testutil.FailErr(t, "write invalid stat", os.WriteFile(path, []byte(bad), 0o644))
		if _, err := verifier.snapshot(42); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, err := parseStatusUIDs([]byte("Name:\tpw\n")); err == nil {
		t.Fatal("accepted a status without Uid")
	}
}

func fixtureNamespace(_ string, pid int) (NamespaceFacts, error) {
	if pid == 0 {
		return NamespaceFacts{Device: 1, Inode: 10}, nil
	}
	return NamespaceFacts{Device: 1, Inode: 20, FullUIDMap: true, FullGIDMap: true, NoNewPrivileges: true}, nil
}
