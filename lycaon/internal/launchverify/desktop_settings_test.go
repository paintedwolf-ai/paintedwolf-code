package launchverify

import "testing"

func TestDesktopSettingsManifestEntryNeverGrantsCustodyRole(t *testing.T) {
	proc, verifier := newFixture(t)
	observer := []byte("separate read-only settings observer")
	verifier.Manifest.Executables = append(verifier.Manifest.Executables,
		entryOf("/opt/painted-wolf-code/bin/pw-desktop-settings", observer))
	proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, image: observer})
	for _, role := range []string{RoleEngine, RoleDesktopApp} {
		if code := CodeOf(verifier.VerifyExecutable(300, role)); code != CodeExecutableMismatch {
			t.Fatalf("observer as %s: refusal = %s", role, code)
		}
	}
	if code := CodeOf(verifier.VerifyExecutable(300, "pw-desktop-settings")); code != CodeManifestUnreadable {
		t.Fatalf("observer cannot define a new custody role: refusal = %s", code)
	}
}
