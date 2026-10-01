package version_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/version"
)

func TestDefaultVersionIsDev(t *testing.T) {
	if version.Version != "dev" {
		t.Fatalf("default Version = %q want dev (ldflags override only for release)", version.Version)
	}
}
