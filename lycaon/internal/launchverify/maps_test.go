package launchverify

import (
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func chainFixture(t *testing.T) (fakeProc, Verifier) {
	t.Helper()
	proc, verifier := newFixture(t)
	proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, start: 10, image: desktopImage})
	proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, start: 20, image: engineImage})
	proc.files["/usr"] = fileFacts{mode: fs.ModeDir | 0o755}
	proc.files["/usr/lib64"] = fileFacts{mode: fs.ModeDir | 0o755}
	proc.files["/usr/lib64/libwebkit2gtk-4.1.so.0"] = fileFacts{device: 0xfd01, inode: 77, mode: 0o755}
	proc.files["/home"] = fileFacts{mode: fs.ModeDir | 0o755}
	proc.files["/home/person"] = fileFacts{uid: 1000, mode: fs.ModeDir | 0o700}
	proc.files["/home/person/inject.so"] = fileFacts{device: 0xfd01, inode: 88, uid: 1000, mode: 0o755}
	proc.files["/srv"] = fileFacts{mode: fs.ModeDir | 0o775}
	proc.files["/srv/shared.so"] = fileFacts{device: 0xfd01, inode: 99, mode: 0o755}
	proc.files["/opt/lib with space.so"] = fileFacts{device: 0xfd01, inode: 55, mode: 0o755}
	proc.files["/opt/painted-wolf-code/link.so"] = fileFacts{device: 0xfd01, inode: 66, mode: fs.ModeSymlink | 0o777}
	return proc, verifier
}

func TestMappingsAcceptSystemLibrariesAndAnonymousCode(t *testing.T) {
	proc, verifier := chainFixture(t)
	proc.appendMaps(300, "7f1000000000-7f1000100000 r-xp 00000000 fd:01 77 /usr/lib64/libwebkit2gtk-4.1.so.0\n"+
		"7f1000100000-7f1000200000 r--p 00100000 fd:01 88 /home/person/inject.so\n"+
		"7f1000200000-7f1000300000 r-xp 00000000 fd:01 55 /opt/lib with space.so\n"+
		"7f1000300000-7f1000400000 r-xp 00000000 fd:01 77 /usr/lib64/libwebkit2gtk-4.1.so.0\n")
	_, err := verifier.VerifyChain(301, 1000)
	testutil.FailErr(t, "verify chain with system libraries", err)
}

func TestMappingsRefuseForeignExecutableObjects(t *testing.T) {
	for name, line := range map[string]string{
		"user preloaded library":      "7f1-7f2 r-xp 00000000 fd:01 88 /home/person/inject.so\n",
		"library in a writable dir":   "7f1-7f2 r-xp 00000000 fd:01 99 /srv/shared.so\n",
		"replaced after mapping":      "7f1-7f2 r-xp 00000000 fd:01 78 /usr/lib64/libwebkit2gtk-4.1.so.0\n",
		"memfd code":                  "7f1-7f2 r-xp 00000000 00:01 4242 /memfd:payload (deleted)\n",
		"deleted library":             "7f1-7f2 r-xp 00000000 fd:01 77 /usr/lib64/gone.so (deleted)\n",
		"unknown file":                "7f1-7f2 r-xp 00000000 fd:01 12 /usr/lib64/missing.so\n",
		"symlink at the mapped path":  "7f1-7f2 r-xp 00000000 fd:01 66 /opt/painted-wolf-code/link.so\n",
		"relative path":               "7f1-7f2 r-xp 00000000 fd:01 12 lib.so\n",
		"truncated line":              "7f1-7f2 r-xp\n",
		"different device same inode": "7f1-7f2 r-xp 00000000 fd:02 77 /usr/lib64/libwebkit2gtk-4.1.so.0\n",
		"bad device":                  "7f1-7f2 r-xp 00000000 nope 77 /usr/lib64/libwebkit2gtk-4.1.so.0\n",
		"bad inode":                   "7f1-7f2 r-xp 00000000 fd:01 x /usr/lib64/libwebkit2gtk-4.1.so.0\n",
	} {
		t.Run(name, func(t *testing.T) {
			proc, verifier := chainFixture(t)
			proc.appendMaps(300, line)
			_, err := verifier.VerifyChain(301, 1000)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodeForeignObject || refusal.Role != RoleDesktopApp {
				t.Fatalf("err = %v, want foreign_object for the desktop app", err)
			}
		})
	}
}

func TestMappingsAreCheckedForTheEngineToo(t *testing.T) {
	proc, verifier := chainFixture(t)
	proc.appendMaps(301, "7f1-7f2 r-xp 00000000 fd:01 88 /home/person/inject.so\n")
	if _, err := verifier.VerifyChain(301, 1000); CodeOf(err) != CodeForeignObject {
		t.Fatalf("err = %v", err)
	}
}

func TestMappingsNeedAReadableMapsFile(t *testing.T) {
	proc, verifier := chainFixture(t)
	testutil.FailErr(t, "remove maps", os.Remove(proc.root+"/300/maps"))
	if err := verifier.VerifyMappings(300, RoleDesktopApp); CodeOf(err) != CodeProcessGone {
		t.Fatalf("err = %v", err)
	}
}

func TestRootOwnershipRules(t *testing.T) {
	if rootOwned("/x", fileFacts{mode: 0o755}) != nil {
		t.Fatal("root 0755 refused")
	}
	if rootOwned("/x", fileFacts{uid: 1, mode: 0o755}) == nil || rootOwned("/x", fileFacts{mode: 0o757}) == nil {
		t.Fatal("foreign or writable object accepted")
	}
	facts, err := lstatFacts("/")
	testutil.FailErr(t, "lstat root", err)
	if !facts.mode.IsDir() {
		t.Fatal("root is not a directory")
	}
	if _, err := lstatFacts("/no/such/path/painted-wolf"); err == nil {
		t.Fatal("missing path statted")
	}
}

func TestMapsPathIsTheRestAfterTheFifthField(t *testing.T) {
	for line, want := range map[string]string{
		"7f1-7f2 r-xp 00000000 fd:01 1234      /opt/app/lib with space.so": "/opt/app/lib with space.so",
		"7f1-7f2 r-xp 00000000 00:00 0 [vdso]":                             "[vdso]",
		"7f1-7f2 r-xp 00000000 00:00 0":                                    "",
		"7f1-7f2 r-xp":                                                     "",
	} {
		if got := mapsPath(line); got != want {
			t.Errorf("mapsPath(%q) = %q, want %q", line, got, want)
		}
	}
}
