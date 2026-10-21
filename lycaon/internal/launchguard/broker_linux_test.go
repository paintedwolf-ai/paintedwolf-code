//go:build linux

package launchguard

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBrokerLooksProgramsUpOnTheRequestPath(t *testing.T) {
	path, err := lookPath("sh", []string{"PATH=relative:/usr/bin:/bin"})
	testutil.FailErr(t, "resolve sh", err)
	if !strings.HasSuffix(path, "/sh") || !strings.HasPrefix(path, "/") {
		t.Fatalf("resolved %q", path)
	}
	if _, err := lookPath("sh", []string{"PATH=relative"}); err == nil {
		t.Fatal("a relative PATH entry resolved a program")
	}
}
