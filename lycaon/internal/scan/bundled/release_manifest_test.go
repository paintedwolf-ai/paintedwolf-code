package bundled_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReleaseManifestRejectsAmbiguousOrUnpinnedSelection(t *testing.T) {
	source := sourceManifest()
	raw := buildJSON(t, source)
	parsed, err := bundled.ParseReleaseManifest(raw)
	testutil.FailErr(t, "parse released descriptor", err)
	pin, err := parsed.ReleaseForPlatform("darwin", "arm64")
	testutil.FailErr(t, "resolve selected pin", err)
	if pin.SHA256 != source.OpenGrep.Artifacts[0].SHA256 {
		t.Fatal("descriptor changed selected pin")
	}
	if _, err := parsed.ReleaseForPlatform("linux", "amd64"); err == nil {
		t.Fatal("missing release platform fell back")
	}
	for name, mutate := range map[string]func(*bundled.Manifest){
		"no artifacts": func(m *bundled.Manifest) { m.OpenGrep.Artifacts = nil },
		"duplicate platform": func(m *bundled.Manifest) {
			m.OpenGrep.Artifacts = append(m.OpenGrep.Artifacts, m.OpenGrep.Artifacts[0])
		},
		"HTTP URL": func(m *bundled.Manifest) { m.OpenGrep.Artifacts[0].URL = "http://example.invalid/release" },
		"credentials": func(m *bundled.Manifest) {
			m.OpenGrep.Artifacts[0].URL = "https://user:password@example.invalid/release"
		},
		"fragment":               func(m *bundled.Manifest) { m.OpenGrep.Artifacts[0].URL += "#different" },
		"bad digest":             func(m *bundled.Manifest) { m.OpenGrep.Artifacts[0].SHA256 = "bad" },
		"empty size":             func(m *bundled.Manifest) { m.OpenGrep.Artifacts[0].Bytes = 0 },
		"oversized":              func(m *bundled.Manifest) { m.OpenGrep.Artifacts[0].Bytes = 1 << 40 },
		"future Windows runtime": func(m *bundled.Manifest) { m.OpenGrep.Artifacts[0].GOOS = "windows" },
	} {
		t.Run(name, func(t *testing.T) {
			m := sourceManifest()
			mutate(m)
			if _, err := bundled.ParseReleaseManifest(buildJSON(t, m)); err == nil {
				t.Fatal("invalid released selection accepted")
			}
		})
	}
	for name, invalid := range map[string][]byte{
		"duplicate field":  bytes.Replace(raw, []byte(`"origin":"downstream"`), []byte(`"origin":"downstream","origin":"downstream"`), 1),
		"unknown field":    append([]byte(`{"unreviewed":true,`), raw[1:]...),
		"multiple objects": append(append([]byte{}, raw...), raw...),
		"null":             []byte("null"),
		"deep nesting":     []byte(strings.Repeat("[", 40) + strings.Repeat("]", 40)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := bundled.ParseReleaseManifest(invalid); err == nil {
				t.Fatal("ambiguous descriptor accepted")
			}
		})
	}
}
