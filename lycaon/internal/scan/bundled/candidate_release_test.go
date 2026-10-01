//go:build paintedwolf_release

package bundled_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
)

func TestReleaseRejectsExplicitOpenGrepCandidate(t *testing.T) {
	t.Setenv(bundled.EnvOpenGrepCandidate, t.TempDir())
	if _, err := bundled.LoadManifest(); err == nil {
		t.Fatal("release build accepted a candidate or fell back to shipping")
	}
}
