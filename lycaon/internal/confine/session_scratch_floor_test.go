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

// When configdir.Root() is attached as the project root, WriteRootsForBoundary
// drops configdir.Root() and keeps SessionScratchRoot. A real confined
// subprocess launched with cmd.Dir set to SessionScratchRoot (the omitted-cwd
// @scratch fallback) executes in @scratch, can write and read back inside
// @scratch, and cannot read or write control-plane state or other sessions' scratch.
func TestAttachedControlPlaneRootSubprocessScratchFallbackAndDenial(t *testing.T) {
	self := requireLiveBoundary(t)
	_, configDir, own, other := sessionScratchFixture(t)
	configDir = fspath.CanonicalPath(configDir)
	own = fspath.CanonicalPath(own)
	other = fspath.CanonicalPath(other)

	stateToken := filepath.Join(configDir, "api.token")
	stateSettings := filepath.Join(configDir, "settings.yaml")
	otherFile := filepath.Join(other, "flow.sh")
	writeFixtureFile(t, stateToken)
	writeFixtureFile(t, stateSettings)
	writeFixtureFile(t, otherFile)

	writeRoots, err := confine.WriteRootsForBoundary("", []string{configDir}, nil, own)
	testutil.FailErr(t, "WriteRootsForBoundary with attached configdir.Root()", err)
	if slices.Contains(writeRoots, configDir) {
		t.Fatalf("WriteRootsForBoundary admitted attached configdir.Root(): %v", writeRoots)
	}
	if !confine.PathWithinWriteRoots(own, writeRoots) {
		t.Fatalf("WriteRootsForBoundary omitted SessionScratchRoot %q: %v", own, writeRoots)
	}

	c := confine.Confinement{
		Roots:              liveBoundaryRoots(configDir),
		SessionScratchRoot: own,
		Network:            confine.NetworkDeny,
		EgressOff:          confine.EgressOffOverride,
	}
	cmd, cleanup, err := confine.Command(t.Context(), self, "/bin/sh", []string{
		"-c",
		`pwd && echo scratch-ok > ./scratch-artifact.txt && cat ./scratch-artifact.txt`,
	}, c)
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

	for _, deniedRead := range []string{stateToken, stateSettings, otherFile} {
		if code := confinedExit(t, self, c, "/bin/cat", deniedRead); code == 0 {
			t.Fatalf("confined subprocess read protected control-plane path %s", deniedRead)
		}
	}
	for _, deniedWrite := range []string{stateToken, filepath.Join(configDir, "injected.txt"), otherFile} {
		if code := confinedExit(t, self, c, "/bin/sh", "-c", `echo pwn > "$1"`, "sh", deniedWrite); code == 0 {
			t.Fatalf("confined subprocess wrote protected control-plane path %s", deniedWrite)
		}
	}
}

// When ~/.ssh is attached as the project root, WriteRootsForBoundary drops ~/.ssh
// and keeps SessionScratchRoot. A real confined subprocess can read public keys
// (*.pub) and write to @scratch, cannot read private keys or write inside ~/.ssh
// without approval, gains exact per-file read/write access when ProtectedReadGrants
// or ProtectedWriteGrants are present, and loses access again once revoked.
func TestAttachedProtectedRootSubprocessGrantAndRevokeLifecycle(t *testing.T) {
	self := requireLiveBoundary(t)
	home := fspath.CanonicalPath(t.TempDir())
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "paintedwolf")
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")

	sshDir := filepath.Join(home, ".ssh")
	ownScratch := enginepaths.SessionScratchUnder(configDir, "chat-ssh")
	for _, dir := range []string{sshDir, ownScratch} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o700))
	}
	idEd25519 := filepath.Join(sshDir, "id_ed25519")
	idRSA := filepath.Join(sshDir, "id_rsa")
	idPub := filepath.Join(sshDir, "id_ed25519.pub")
	authKeys := filepath.Join(sshDir, "authorized_keys")
	evilFile := filepath.Join(sshDir, "evil.txt")
	testutil.FailErr(t, "write id_ed25519", os.WriteFile(idEd25519, []byte("private-ed25519\n"), 0o600))
	testutil.FailErr(t, "write id_rsa", os.WriteFile(idRSA, []byte("private-rsa\n"), 0o600))
	testutil.FailErr(t, "write id_ed25519.pub", os.WriteFile(idPub, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA\n"), 0o644))
	testutil.FailErr(t, "write authorized_keys", os.WriteFile(authKeys, []byte("initial-key\n"), 0o600))

	writeRoots, err := confine.WriteRootsForBoundary("", []string{sshDir}, nil, ownScratch)
	testutil.FailErr(t, "WriteRootsForBoundary with attached ~/.ssh", err)
	if slices.Contains(writeRoots, sshDir) {
		t.Fatalf("WriteRootsForBoundary admitted attached ~/.ssh: %v", writeRoots)
	}
	if !confine.PathWithinWriteRoots(ownScratch, writeRoots) {
		t.Fatalf("WriteRootsForBoundary omitted SessionScratchRoot %q: %v", ownScratch, writeRoots)
	}

	base := confine.Confinement{
		Roots:              []string{sshDir},
		SessionScratchRoot: ownScratch,
		Network:            confine.NetworkDeny,
		EgressOff:          confine.EgressOffOverride,
	}

	// 1. Unapproved baseline: public key and @scratch work; private keys and ~/.ssh writes are denied.
	if code, out := confinedRun(t, self, base, "/bin/cat", idPub); code != 0 || !strings.Contains(out, "ssh-ed25519") {
		t.Fatalf("unapproved public key read exit=%d out=%q, want 0", code, out)
	}
	scratchNote := filepath.Join(ownScratch, "note.txt")
	if code := confinedExit(t, self, base, "/bin/sh", "-c", `echo scratch-ok > "$1"`, "sh", scratchNote); code != 0 {
		t.Fatalf("unapproved @scratch write exit=%d, want 0", code)
	}
	for _, secret := range []string{idEd25519, idRSA} {
		if code := confinedExit(t, self, base, "/bin/cat", secret); code == 0 {
			t.Fatalf("unapproved confined subprocess read private key %s", secret)
		}
	}
	for _, target := range []string{authKeys, idEd25519, evilFile} {
		if code := confinedExit(t, self, base, "/bin/sh", "-c", `echo pwn > "$1"`, "sh", target); code == 0 {
			t.Fatalf("unapproved confined subprocess wrote %s inside ~/.ssh", target)
		}
	}

	// 2. Exact read grant on id_ed25519: reading id_ed25519 succeeds; sibling id_rsa and writes remain denied.
	withReadGrant := base
	withReadGrant.ProtectedReadGrants = []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(idEd25519)}
	if code, out := confinedRun(t, self, withReadGrant, "/bin/cat", idEd25519); code != 0 || out != "private-ed25519\n" {
		t.Fatalf("approved id_ed25519 read exit=%d out=%q, want 0 and \"private-ed25519\\n\"", code, out)
	}
	if code := confinedExit(t, self, withReadGrant, "/bin/cat", idRSA); code == 0 {
		t.Fatal("exact read grant on id_ed25519 leaked sibling id_rsa")
	}
	if code := confinedExit(t, self, withReadGrant, "/bin/sh", "-c", `echo pwn > "$1"`, "sh", idEd25519); code == 0 {
		t.Fatal("exact read grant on id_ed25519 permitted writing id_ed25519")
	}

	// 3. Exact write grant on authorized_keys: writing authorized_keys succeeds; sibling files remain write-denied.
	withWriteGrant := base
	withWriteGrant.ProtectedWriteGrants = []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(authKeys)}
	if code := confinedExit(t, self, withWriteGrant, "/bin/sh", "-c", `printf "approved-key\n" > "$1"`, "sh", authKeys); code != 0 {
		t.Fatalf("approved authorized_keys write exit=%d, want 0", code)
	}
	if got, err := os.ReadFile(authKeys); err != nil || string(got) != "approved-key\n" {
		t.Fatalf("authorized_keys content = %q (%v), want \"approved-key\\n\"", got, err)
	}
	for _, unapprovedTarget := range []string{idEd25519, evilFile} {
		if code := confinedExit(t, self, withWriteGrant, "/bin/sh", "-c", `echo pwn > "$1"`, "sh", unapprovedTarget); code == 0 {
			t.Fatalf("exact write grant on authorized_keys permitted writing %s", unapprovedTarget)
		}
	}

	// 4. Revocation: running again with base confinement denies reading id_ed25519 and writing authorized_keys.
	if code := confinedExit(t, self, base, "/bin/cat", idEd25519); code == 0 {
		t.Fatal("revoked read grant still allowed reading id_ed25519")
	}
	if code := confinedExit(t, self, base, "/bin/sh", "-c", `printf "tampered\n" > "$1"`, "sh", authKeys); code == 0 {
		t.Fatal("revoked write grant still allowed writing authorized_keys")
	}
	if got, err := os.ReadFile(authKeys); err != nil || string(got) != "approved-key\n" {
		t.Fatalf("authorized_keys content after revoked write attempt = %q (%v), want \"approved-key\\n\"", got, err)
	}
}
