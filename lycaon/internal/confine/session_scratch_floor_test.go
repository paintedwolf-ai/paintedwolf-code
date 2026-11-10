package confine_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// sessionScratchFixture isolates the control plane under a temporary home and
// returns a confinement whose own scratch is one of two session scratch roots.
func sessionScratchFixture(t *testing.T) (c confine.Confinement, configDir, own, other string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir = filepath.Join(home, ".config", "paintedwolf")
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	own = enginepaths.SessionScratchUnder(configDir, "chat-own")
	other = enginepaths.SessionScratchUnder(configDir, "chat-other")
	for _, dir := range []string{own, other} {
		testutil.FailErr(t, "create scratch", os.MkdirAll(dir, 0o700))
	}
	c = confine.Confinement{
		Roots:              []string{t.TempDir()},
		SessionScratchRoot: own,
	}
	return c, configDir, own, other
}

// A session's own scratch is its workspace in both lanes: what a command
// writes there it can read back. Every other session's scratch and the rest
// of the control plane stay behind the read floor.
func TestSessionScratchIsReadableAndWritableByItsOwnSession(t *testing.T) {
	c, configDir, own, other := sessionScratchFixture(t)
	writeFixtureFile(t, filepath.Join(configDir, "store.db"))
	boundary := confine.BoundaryOf(&c)
	if !boundary.Applied {
		t.Fatal("scratch confinement did not project an applied boundary")
	}
	allowed := confine.FloorVerdict{Allowed: true}
	controlPlaneRead := confine.FloorVerdict{Layer: confine.FloorReadControlPlane}
	controlPlaneWrite := confine.FloorVerdict{Layer: confine.FloorControlPlane}
	for _, probe := range []boundaryProbe{
		{name: "own scratch write", access: confine.AccessWrite, path: filepath.Join(own, "flow.sh"), want: allowed},
		{name: "own scratch read", access: confine.AccessRead, path: filepath.Join(own, "flow.sh"), exists: true, want: allowed},
		{name: "other session scratch write", access: confine.AccessWrite, path: filepath.Join(other, "flow.sh"), want: controlPlaneWrite},
		{name: "other session scratch read", access: confine.AccessRead, path: filepath.Join(other, "flow.sh"), exists: true, want: controlPlaneRead},
		{name: "control plane store read", access: confine.AccessRead, path: filepath.Join(configDir, "store.db"), exists: true, want: controlPlaneRead},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if probe.exists {
				writeFixtureFile(t, probe.path)
			}
			if got := boundary.Filesystem.Verdict(probe.access, probe.path); got != probe.want {
				t.Fatalf("%s %s = %+v, want %+v", probe.access, probe.path, got, probe.want)
			}
		})
	}
}

// The kernel agrees: a confined command writes its own scratch and reads the
// file back, while another session's scratch stays unreadable.
func TestBoundaryLetsSessionReadBackItsOwnScratch(t *testing.T) {
	self := requireLiveBoundary(t)
	c, _, own, other := sessionScratchFixture(t)
	ownFile := filepath.Join(own, "flow.sh")
	if code := confinedExit(t, self, c, "/bin/sh", "-c", `echo ok > "$1"`, "sh", ownFile); code != 0 {
		t.Fatalf("own scratch write exit = %d, want 0", code)
	}
	if code, out := confinedRun(t, self, c, "/bin/cat", ownFile); code != 0 || out != "ok\n" {
		t.Fatalf("own scratch read exit = %d, out = %q; want 0 and \"ok\\n\"", code, out)
	}
	otherFile := filepath.Join(other, "flow.sh")
	writeFixtureFile(t, otherFile)
	if code := confinedExit(t, self, c, "/bin/cat", otherFile); code == 0 {
		t.Fatal("other session scratch read exit = 0, want denied")
	}
}

// When configdir.Root() is attached as the project root, DefaultConfinement
// builds an applied boundary whose WriteRootsForBoundary drops configdir.Root()
// and keeps SessionScratchRoot. A real confined subprocess launched with cmd.Dir
// set to SessionScratchRoot executes in @scratch, can write and read back inside
// @scratch, and cannot read, write, rmdir, or mv control-plane state or other
// sessions' scratch.
func TestAttachedControlPlaneRootSubprocessScratchFallbackAndDenial(t *testing.T) {
	self := requireLiveBoundary(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	_, configDir, own, other := sessionScratchFixture(t)
	configDir = fspath.CanonicalPath(configDir)
	own = fspath.CanonicalPath(own)
	other = fspath.CanonicalPath(other)

	stateToken := filepath.Join(configDir, "api.token")
	stateSettings := filepath.Join(configDir, "settings.yaml")
	stateStore := filepath.Join(configDir, "store.db")
	otherFile := filepath.Join(other, "flow.sh")
	for _, f := range []string{stateToken, stateSettings, stateStore, otherFile} {
		writeFixtureFile(t, f)
	}

	c, applied := confine.DefaultConfinement(confine.Request{
		Roots:              liveBoundaryRoots(configDir),
		SessionScratchRoot: own,
		Egress:             confine.EgressDeny,
	})
	if !applied || c == nil {
		t.Fatalf("DefaultConfinement with attached configdir.Root() not applied: applied=%v c=%+v", applied, c)
	}
	boundary := confine.BoundaryOf(c)
	if !boundary.Applied {
		t.Fatal("BoundaryOf did not report Applied=true")
	}
	if slices.Contains(boundary.WriteRoots, configDir) {
		t.Fatalf("BoundaryOf.WriteRoots admitted attached configdir.Root(): %v", boundary.WriteRoots)
	}
	if !confine.PathWithinWriteRoots(own, boundary.WriteRoots) {
		t.Fatalf("BoundaryOf.WriteRoots omitted SessionScratchRoot %q: %v", own, boundary.WriteRoots)
	}

	cmd, cleanup, err := confine.Command(t.Context(), self, "/bin/sh", []string{
		"-c",
		`pwd && echo scratch-ok > ./scratch-artifact.txt && cat ./scratch-artifact.txt`,
	}, *c)
	testutil.FailErr(t, "confine.Command in @scratch fallback cwd", err)
	cmd.Dir = own
	out, runErr := cmd.CombinedOutput()
	cleanup()
	if runErr != nil {
		t.Fatalf("confined command in @scratch fallback cwd failed: %v\n%s", runErr, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || fspath.CanonicalPath(lines[0]) != own || lines[1] != "scratch-ok" {
		t.Fatalf("confined @scratch output = %q, want pwd=%q and \"scratch-ok\"", string(out), own)
	}

	for _, deniedRead := range []string{stateToken, stateSettings, stateStore, otherFile} {
		if code := confinedExit(t, self, *c, "/bin/cat", deniedRead); code == 0 {
			t.Fatalf("confined subprocess read protected control-plane path %s", deniedRead)
		}
	}
	for _, deniedWrite := range []string{stateToken, stateSettings, stateStore, filepath.Join(configDir, "injected.txt"), otherFile} {
		if code := confinedExit(t, self, *c, "/bin/sh", "-c", `echo pwn > "$1"`, "sh", deniedWrite); code == 0 {
			t.Fatalf("confined subprocess wrote protected control-plane path %s", deniedWrite)
		}
	}
	if code := confinedExit(t, self, *c, "/bin/rmdir", configDir); code == 0 {
		t.Fatal("confined subprocess rmdir configdir.Root() succeeded")
	}
	if code := confinedExit(t, self, *c, "/bin/mv", configDir, configDir+".moved"); code == 0 {
		t.Fatal("confined subprocess mv configdir.Root() succeeded")
	}
}

// When ~/.ssh is attached alongside an ordinary directory, DefaultConfinement
// builds an applied boundary whose WriteRootsForBoundary drops ~/.ssh while
// retaining the ordinary directory and SessionScratchRoot. A real confined
// subprocess can read ordinary files outside the ~/.ssh floor and read/write
// inside @scratch, cannot read or write inside ~/.ssh (including .pub) without
// approval, gains exact per-file read/write access when ProtectedReadGrants or
// ProtectedWriteGrants are present, and loses access again once revoked.
func TestAttachedProtectedRootSubprocessGrantAndRevokeLifecycle(t *testing.T) {
	self := requireLiveBoundary(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	home := fspath.CanonicalPath(t.TempDir())
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "paintedwolf")
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")

	sshDir := filepath.Join(home, ".ssh")
	ordinaryDir := filepath.Join(home, "workspace")
	ownScratch := enginepaths.SessionScratchUnder(configDir, "chat-ssh")
	for _, dir := range []string{sshDir, ordinaryDir, ownScratch} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o700))
	}
	ordinaryReadme := filepath.Join(ordinaryDir, "readme.txt")
	idEd25519 := filepath.Join(sshDir, "id_ed25519")
	idEd25519Pub := filepath.Join(sshDir, "id_ed25519.pub")
	idRSA := filepath.Join(sshDir, "id_rsa")
	authKeys := filepath.Join(sshDir, "authorized_keys")
	authKeysTest := filepath.Join(sshDir, "authorized_keys_test")
	evilFile := filepath.Join(sshDir, "evil.txt")
	testutil.FailErr(t, "write readme.txt", os.WriteFile(ordinaryReadme, []byte("ordinary-readme\n"), 0o600))
	testutil.FailErr(t, "write id_ed25519", os.WriteFile(idEd25519, []byte("private-ed25519\n"), 0o600))
	testutil.FailErr(t, "write id_ed25519.pub", os.WriteFile(idEd25519Pub, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI test\n"), 0o644))
	testutil.FailErr(t, "write id_rsa", os.WriteFile(idRSA, []byte("private-rsa\n"), 0o600))
	testutil.FailErr(t, "write authorized_keys", os.WriteFile(authKeys, []byte("initial-key\n"), 0o600))
	testutil.FailErr(t, "write authorized_keys_test", os.WriteFile(authKeysTest, []byte("initial-test-key\n"), 0o600))

	buildConf := func(readGrants, writeGrants []confine.ProtectedPathGrant) confine.Confinement {
		t.Helper()
		c, applied := confine.DefaultConfinement(confine.Request{
			Roots:                liveBoundaryRoots(sshDir, ordinaryDir),
			SessionScratchRoot:   ownScratch,
			ProtectedReadGrants:  readGrants,
			ProtectedWriteGrants: writeGrants,
			Egress:               confine.EgressDeny,
		})
		if !applied || c == nil {
			t.Fatalf("DefaultConfinement with attached ~/.ssh not applied: applied=%v c=%+v", applied, c)
		}
		boundary := confine.BoundaryOf(c)
		if !boundary.Applied {
			t.Fatal("BoundaryOf did not report Applied=true")
		}
		if slices.Contains(boundary.WriteRoots, sshDir) {
			t.Fatalf("BoundaryOf.WriteRoots admitted attached ~/.ssh: %v", boundary.WriteRoots)
		}
		if !slices.Contains(boundary.WriteRoots, ordinaryDir) {
			t.Fatalf("BoundaryOf.WriteRoots omitted ordinary root %q: %v", ordinaryDir, boundary.WriteRoots)
		}
		if !confine.PathWithinWriteRoots(ownScratch, boundary.WriteRoots) {
			t.Fatalf("BoundaryOf.WriteRoots omitted SessionScratchRoot %q: %v", ownScratch, boundary.WriteRoots)
		}
		return *c
	}

	base := buildConf(nil, nil)

	// 1. Unapproved baseline: ordinary readable files outside ~/.ssh and @scratch work;
	// ~/.ssh reads (including .pub under the catalogued .ssh floor) and writes are denied.
	if code, out := confinedRun(t, self, base, "/bin/cat", ordinaryReadme); code != 0 || out != "ordinary-readme\n" {
		t.Fatalf("unapproved ordinary file read exit=%d out=%q, want 0 and \"ordinary-readme\\n\"", code, out)
	}
	scratchNote := filepath.Join(ownScratch, "note.txt")
	if code := confinedExit(t, self, base, "/bin/sh", "-c", `echo scratch-ok > "$1"`, "sh", scratchNote); code != 0 {
		t.Fatalf("unapproved @scratch write exit=%d, want 0", code)
	}
	if code, out := confinedRun(t, self, base, "/bin/cat", scratchNote); code != 0 || out != "scratch-ok\n" {
		t.Fatalf("unapproved @scratch read exit=%d out=%q, want 0 and \"scratch-ok\\n\"", code, out)
	}
	for _, secret := range []string{idEd25519, idEd25519Pub, idRSA, authKeys} {
		if code := confinedExit(t, self, base, "/bin/cat", secret); code == 0 {
			t.Fatalf("unapproved confined subprocess read key-material file %s", secret)
		}
	}
	for _, target := range []string{authKeys, authKeysTest, idEd25519, evilFile} {
		if code := confinedExit(t, self, base, "/bin/sh", "-c", `echo pwn > "$1"`, "sh", target); code == 0 {
			t.Fatalf("unapproved confined subprocess wrote %s inside ~/.ssh", target)
		}
	}

	// 2. Exact read grant on id_ed25519: reading id_ed25519 succeeds; sibling files (id_rsa, id_ed25519.pub) and writes remain denied.
	withReadGrant := buildConf([]confine.ProtectedPathGrant{confine.NewProtectedPathGrant(idEd25519)}, nil)
	if code, out := confinedRun(t, self, withReadGrant, "/bin/cat", idEd25519); code != 0 || out != "private-ed25519\n" {
		t.Fatalf("approved id_ed25519 read exit=%d out=%q, want 0 and \"private-ed25519\\n\"", code, out)
	}
	for _, unapprovedRead := range []string{idRSA, idEd25519Pub, authKeys} {
		if code := confinedExit(t, self, withReadGrant, "/bin/cat", unapprovedRead); code == 0 {
			t.Fatalf("exact read grant on id_ed25519 leaked sibling %s", unapprovedRead)
		}
	}
	if code := confinedExit(t, self, withReadGrant, "/bin/sh", "-c", `echo pwn > "$1"`, "sh", idEd25519); code == 0 {
		t.Fatal("exact read grant on id_ed25519 permitted writing id_ed25519")
	}

	// 3. Exact write grant on authorized_keys and authorized_keys_test: writing them succeeds; sibling files remain write-denied.
	withWriteGrant := buildConf(nil, []confine.ProtectedPathGrant{
		confine.NewProtectedPathGrant(authKeys),
		confine.NewProtectedPathGrant(authKeysTest),
	})
	for _, grantedTarget := range []string{authKeys, authKeysTest} {
		if code := confinedExit(t, self, withWriteGrant, "/bin/sh", "-c", `printf "approved-key\n" > "$1"`, "sh", grantedTarget); code != 0 {
			t.Fatalf("approved %s write exit=%d, want 0", grantedTarget, code)
		}
		if got, err := os.ReadFile(grantedTarget); err != nil || string(got) != "approved-key\n" {
			t.Fatalf("%s content = %q (%v), want \"approved-key\\n\"", grantedTarget, got, err)
		}
	}
	for _, unapprovedTarget := range []string{idEd25519, evilFile} {
		if code := confinedExit(t, self, withWriteGrant, "/bin/sh", "-c", `echo pwn > "$1"`, "sh", unapprovedTarget); code == 0 {
			t.Fatalf("exact write grant permitted writing unapproved %s", unapprovedTarget)
		}
	}

	// 4. Revocation: rebuilding confinement without grants denies reading id_ed25519 and writing authorized_keys / authorized_keys_test.
	revoked := buildConf(nil, nil)
	if code := confinedExit(t, self, revoked, "/bin/cat", idEd25519); code == 0 {
		t.Fatal("revoked read grant still allowed reading id_ed25519")
	}
	for _, revokedTarget := range []string{authKeys, authKeysTest} {
		if code := confinedExit(t, self, revoked, "/bin/sh", "-c", `printf "tampered\n" > "$1"`, "sh", revokedTarget); code == 0 {
			t.Fatalf("revoked write grant still allowed writing %s", revokedTarget)
		}
		if got, err := os.ReadFile(revokedTarget); err != nil || string(got) != "approved-key\n" {
			t.Fatalf("%s content after revoked write attempt = %q (%v), want \"approved-key\\n\"", revokedTarget, got, err)
		}
	}
}
