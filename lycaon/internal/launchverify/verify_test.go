package launchverify

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// fakeProc builds a synthetic process filesystem. Each process maps its
// image from a root-owned installed path that the fake filesystem knows.
type fakeProc struct {
	t     *testing.T
	root  string
	files map[string]fileFacts
}

func (f fakeProc) stat(path string) (fileFacts, error) {
	if facts, ok := f.files[path]; ok {
		return facts, nil
	}
	return fileFacts{}, os.ErrNotExist
}

type fakeProcess struct {
	pid, ppid, uid, euid int
	start                uint64
	image                []byte
}

func (f fakeProc) add(p fakeProcess) {
	dir := filepath.Join(f.root, strconv.Itoa(p.pid))
	testutil.FailErr(f.t, "create proc dir", os.MkdirAll(dir, 0o755))
	stat := fmt.Sprintf("%d (pw (sidecar)) S %d %d %d 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 %d 1 1 1", p.pid, p.ppid, p.pid, p.pid, p.start)
	testutil.FailErr(f.t, "write stat", os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644))
	status := fmt.Sprintf("Name:\tpw\nPPid:\t%d\nUid:\t%d\t%d\t%d\t%d\nGid:\t1000\t1000\t1000\t1000\n", p.ppid, p.uid, p.euid, p.uid, p.euid)
	testutil.FailErr(f.t, "write status", os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644))
	image := filepath.Join(f.root, "images", strconv.Itoa(p.pid))
	testutil.FailErr(f.t, "create image dir", os.MkdirAll(filepath.Dir(image), 0o755))
	testutil.FailErr(f.t, "write image", os.WriteFile(image, p.image, 0o755))
	testutil.FailErr(f.t, "link exe", os.Symlink(image, filepath.Join(dir, "exe")))
	installed := fmt.Sprintf("/opt/painted-wolf-code/image-%d", p.pid)
	f.files[installed] = fileFacts{inode: uint64(p.pid), mode: 0o755}
	maps := fmt.Sprintf("00400000-00500000 r-xp 00000000 fd:01 %d %s\n"+
		"00600000-00700000 rw-p 00000000 00:00 0 \n"+
		"7f0000000000-7f0000001000 r-xp 00000000 00:00 0 \n"+
		"7fff00000000-7fff00002000 r-xp 00000000 00:00 0                          [vdso]\n", p.pid, installed)
	testutil.FailErr(f.t, "write maps", os.WriteFile(filepath.Join(dir, "maps"), []byte(maps), 0o644))
}

func (f fakeProc) appendMaps(pid int, lines string) {
	path := filepath.Join(f.root, strconv.Itoa(pid), "maps")
	raw, err := os.ReadFile(path)
	testutil.FailErr(f.t, "read maps", err)
	testutil.FailErr(f.t, "append maps", os.WriteFile(path, append(raw, lines...), 0o644))
}

func digestOf(image []byte) string {
	sum := sha256.Sum256(image)
	return DigestString(sum[:])
}

var (
	engineImage  = []byte("engine image bytes")
	desktopImage = []byte("desktop app image bytes")
)

func installedManifest() Manifest {
	return Manifest{Version: "1.0.2", Executables: map[string]string{
		"pw":                digestOf(engineImage),
		"painted-wolf-code": digestOf(desktopImage),
		"pw-logs":           digestOf([]byte("logs")),
	}}
}

func newFixture(t *testing.T) (fakeProc, Verifier) {
	t.Helper()
	root := t.TempDir()
	proc := fakeProc{t: t, root: root, files: map[string]fileFacts{
		"/":                      {mode: fs.ModeDir | 0o755},
		"/opt":                   {mode: fs.ModeDir | 0o755},
		"/opt/painted-wolf-code": {mode: fs.ModeDir | 0o755},
	}}
	return proc, Verifier{ProcRoot: root, Manifest: installedManifest(), statFile: proc.stat}
}

func TestVerifyChainAcceptsTheInstalledEngineUnderTheInstalledApp(t *testing.T) {
	proc, verifier := newFixture(t)
	proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, start: 10, image: desktopImage})
	proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, start: 20, image: engineImage})
	chain, err := verifier.VerifyChain(301, 1000)
	testutil.FailErr(t, "verify chain", err)
	if chain.Engine.PID != 301 || chain.Launcher.PID != 300 || chain.Engine.StartTime != 20 {
		t.Fatalf("chain = %+v", chain)
	}
	testutil.FailErr(t, "recheck chain", verifier.Recheck(chain))
}

func TestVerifyChainRefusesWithStructuredCodes(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(fakeProc)
		uid   int
		code  Code
		role  string
	}{
		{name: "unknown pid", setup: func(fakeProc) {}, uid: 1000, code: CodeProcessGone, role: RoleEngine},
		{name: "another user's engine", uid: 1001, code: CodeUIDMismatch, role: RoleEngine, setup: func(p fakeProc) {
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: desktopImage})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "setuid engine", uid: 1000, code: CodeUIDMismatch, role: RoleEngine, setup: func(p fakeProc) {
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 0, image: engineImage})
		}},
		{name: "orphaned engine", uid: 1000, code: CodeNoParent, role: RoleEngine, setup: func(p fakeProc) {
			p.add(fakeProcess{pid: 301, ppid: 1, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "parent exited", uid: 1000, code: CodeProcessGone, role: RoleDesktopApp, setup: func(p fakeProc) {
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "parent owned by another user", uid: 1000, code: CodeUIDMismatch, role: RoleDesktopApp, setup: func(p fakeProc) {
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 0, euid: 0, image: desktopImage})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "modified engine", uid: 1000, code: CodeExecutableMismatch, role: RoleEngine, setup: func(p fakeProc) {
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: desktopImage})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: []byte("patched engine")})
		}},
		{name: "shell is not the desktop app", uid: 1000, code: CodeExecutableMismatch, role: RoleDesktopApp, setup: func(p fakeProc) {
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: []byte("bash")})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "another installed executable as the parent", uid: 1000, code: CodeExecutableMismatch, role: RoleDesktopApp, setup: func(p fakeProc) {
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: []byte("logs")})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
		}},
		{name: "unreadable engine image", uid: 1000, code: CodeExecutableUnreadable, role: RoleEngine, setup: func(p fakeProc) {
			p.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: desktopImage})
			p.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
			testutil.FailErr(p.t, "remove image", os.Remove(filepath.Join(p.root, "images", "301")))
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
	verifier.Manifest = Manifest{Executables: map[string]string{"pw": digestOf(engineImage)}}
	proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: desktopImage})
	proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, image: engineImage})
	if _, err := verifier.VerifyChain(301, 1000); CodeOf(err) != CodeManifestUnreadable {
		t.Fatalf("err = %v", err)
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

func TestParseStatHandlesCommandNamesWithSpacesAndParentheses(t *testing.T) {
	snap, err := parseStat([]byte("42 (a (b) c) S 7 42 42 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 12345 1 1 1"))
	testutil.FailErr(t, "parse stat", err)
	if snap.PID != 42 || snap.PPID != 7 || snap.StartTime != 12345 {
		t.Fatalf("snapshot = %+v", snap)
	}
	for _, bad := range []string{"", "42 S 7", "42 (x) S 7 1", "x (x) S 7 42 42 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 1 1 1 1"} {
		if _, err := parseStat([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, _, err := parseStatusUIDs([]byte("Name:\tpw\n")); err == nil {
		t.Fatal("accepted a status without Uid")
	}
}

func TestLoadManifestValidatesShape(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, value any) string {
		raw, err := json.Marshal(value)
		testutil.FailErr(t, "encode manifest", err)
		p := filepath.Join(dir, name)
		testutil.FailErr(t, "write manifest", os.WriteFile(p, raw, 0o644))
		return p
	}
	good, err := LoadManifest(write("good.json", installedManifest()))
	testutil.FailErr(t, "load manifest", err)
	if sum, ok := good.Digest(RoleEngine); !ok || DigestString(sum) != digestOf(engineImage) {
		t.Fatalf("engine digest = %x, %v", sum, ok)
	}
	if _, ok := good.Digest("missing"); ok {
		t.Fatal("found a missing role")
	}
	for name, value := range map[string]any{
		"empty":    map[string]any{"version": "1"},
		"absolute": map[string]any{"executables": map[string]string{"/opt/pw": digestOf(engineImage)}},
		"dotdot":   map[string]any{"executables": map[string]string{"../pw": digestOf(engineImage)}},
		"md5":      map[string]any{"executables": map[string]string{"pw": "md5:00"}},
		"short":    map[string]any{"executables": map[string]string{"pw": "sha256:abcd"}},
		"nothex":   map[string]any{"executables": map[string]string{"pw": "sha256:" + string(make([]byte, 64))}},
	} {
		if _, err := LoadManifest(write(name+".json", value)); err == nil {
			t.Fatalf("%s manifest accepted", name)
		}
	}
	if _, err := LoadManifest(filepath.Join(dir, "absent.json")); err == nil {
		t.Fatal("absent manifest accepted")
	}
	testutil.FailErr(t, "write junk", os.WriteFile(filepath.Join(dir, "junk.json"), []byte("{"), 0o644))
	if _, err := LoadManifest(filepath.Join(dir, "junk.json")); err == nil {
		t.Fatal("junk manifest accepted")
	}
}
