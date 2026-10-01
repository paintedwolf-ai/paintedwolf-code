package contract

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The artifact gate must run before anything is published, and must not be
// softened with `|| true` — a swallowed verification reads as proof.
func TestArtifactGateRunsBeforeAnyPublish(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	workflow := contractcheck.ReadRepoFile(t, root, ".github/workflows/release.yml")

	verifyAt := strings.Index(workflow, "Verify and stage platform artifacts")
	if verifyAt < 0 {
		t.Fatal(".github/workflows/release.yml has no platform artifact gate")
	}

	for _, publishStep := range []string{"Publish immutable release objects", "Activate release channel last"} {
		publishAt := strings.Index(workflow, publishStep)
		if publishAt < 0 {
			t.Fatalf("release.yml has no %q step", publishStep)
		}
		if publishAt < verifyAt {
			t.Fatalf("%q runs before bundle:verify: nothing may be published before the artifact is audited", publishStep)
		}
	}

	stageScript := contractcheck.ReadRepoFile(t, root, "scripts/release-stage-platform-artifacts.sh")
	if !strings.Contains(stageScript, "bundle:verify -- --app") || !strings.Contains(stageScript, "--dmg") {
		t.Fatal("the platform artifact gate never verifies with --dmg: the notarization staple lives on the .dmg, " +
			"which is what the user actually downloads")
	}
	for _, linuxProof := range []string{"--appimage-extract", "AppRun", "bundled engine sidecar"} {
		if !strings.Contains(stageScript, linuxProof) {
			t.Fatalf("the platform artifact gate lacks Linux structural proof %q", linuxProof)
		}
	}

	bundleScript := contractcheck.ReadRepoFile(t, root, "scripts/den-build-bundle.sh")
	if !strings.Contains(bundleScript, "verify-bundle.sh") {
		t.Fatal("scripts/den-build-bundle.sh no longer verifies the bundle it just built")
	}
	if strings.Contains(bundleScript, "verify-bundle.sh\" \"${VERIFY_ARGS[@]}\" || true") {
		t.Fatal("the bundle verification is softened with `|| true`: a swallowed verification reads as proof")
	}
}
