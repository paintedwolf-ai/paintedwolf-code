package bundled_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceManifestNeedsBuildIdentityOnlyForResolution(t *testing.T) {
	m := sourceManifest()
	testutil.FailErr(t, "validate source selection", bundled.ValidateManifest(m))
	if _, err := m.ArtifactForCurrentPlatform(); err == nil || !strings.Contains(err.Error(), "build identity is absent") {
		t.Fatalf("missing identity result: %v", err)
	}
	if _, err := bundled.ResolveOpenGrepBinary(m, t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("resolution without build authority succeeded")
	}
}

func TestManifestRejectsAmbiguousSourceIdentities(t *testing.T) {
	cases := map[string]func(*bundled.Manifest){"unlocked source": func(m *bundled.Manifest) { m.OpenGrep.SourceLockSHA256 = "" }, "nonhex lock": func(m *bundled.Manifest) { m.OpenGrep.SourceLockSHA256 = strings.Repeat("z", 64) }, "upstream fallback": func(m *bundled.Manifest) { m.OpenGrep.Origin = "upstream" }, "missing revision": func(m *bundled.Manifest) { m.OpenGrep.Revision = 0 }, "mismatched revision": func(m *bundled.Manifest) { m.OpenGrep.Revision++ }, "traversing version": func(m *bundled.Manifest) { m.OpenGrep.Version = "../../escape" }, "missing license": func(m *bundled.Manifest) { m.OpenGrep.License = "" }, "short git revision": func(m *bundled.Manifest) { m.OpenGrep.BaseRevision = "abc" }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := sourceManifest()
			mutate(m)
			if err := bundled.ValidateManifest(m); err == nil {
				t.Fatal("invalid source selection accepted")
			}
		})
	}
}

func TestBuildIdentityRejectsInvalidAuthority(t *testing.T) {
	f := newBuildFixture(t)
	encoded, err := bundled.EncodedBuildIdentity(f.selectArtifact(t))
	testutil.FailErr(t, "encode identity", err)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	testutil.FailErr(t, "decode fixture identity", err)
	cases := map[string]func(*bundled.BuildIdentity){"schema": func(i *bundled.BuildIdentity) { i.SchemaVersion = 2 }, "source": func(i *bundled.BuildIdentity) { i.SourceLockSHA256 = strings.Repeat("c", 64) }, "version": func(i *bundled.BuildIdentity) { i.Version = "1.29.0+paintedwolf.27" }, "platform": func(i *bundled.BuildIdentity) { i.GOOS = "other" }, "digest": func(i *bundled.BuildIdentity) { i.BinarySHA256 = "bad" }, "size": func(i *bundled.BuildIdentity) { i.BinaryBytes = 0 }, "missing payload": func(i *bundled.BuildIdentity) { i.Payload = i.Payload[:1] }, "traversing payload": func(i *bundled.BuildIdentity) { i.Payload[0].Name = "../outside" }, "duplicate payload": func(i *bundled.BuildIdentity) { i.Payload[1] = i.Payload[0] }, "payload source": func(i *bundled.BuildIdentity) { i.Payload[1].SHA256 = strings.Repeat("d", 64) }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var identity bundled.BuildIdentity
			testutil.FailErr(t, "parse fixture", json.Unmarshal(raw, &identity))
			mutate(&identity)
			if _, err := bundled.ManifestWithBuildIdentity(f.source, base64.StdEncoding.EncodeToString(buildJSON(t, identity))); err == nil {
				t.Fatal("invalid build authority accepted")
			}
		})
	}
	for _, bad := range []string{"bad base64", base64.StdEncoding.EncodeToString(append(raw, []byte(" {}")...)), base64.StdEncoding.EncodeToString(append([]byte(`{"unowned":true,`), raw[1:]...))} {
		if _, err := bundled.ManifestWithBuildIdentity(f.source, bad); err == nil {
			t.Fatal("malformed build identity accepted")
		}
	}
}
