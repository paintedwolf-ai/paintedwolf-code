package bundled

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadReleaseWithoutLinkedArtifactIdentity(t *testing.T) {
	previous := buildIdentityBase64
	t.Cleanup(func() { buildIdentityBase64 = previous })
	buildIdentityBase64 = ""
	t.Setenv(EnvOpenGrepCandidate, "")
	m, err := LoadManifest()
	testutil.FailErr(t, "read pinned release manifest", err)
	if m.OpenGrep.Origin != "downstream" || m.OpenGrep.SourceLockSHA256 == "" {
		t.Fatal("release source selection missing")
	}
	if _, err := m.ArtifactForCurrentPlatform(); err == nil || !strings.Contains(err.Error(), "build identity is absent") {
		t.Fatalf("missing build identity: %v", err)
	}
	buildIdentityBase64 = "corrupt"
	if _, err := LoadManifest(); err == nil {
		t.Fatal("corrupt linked identity ignored")
	}
}
