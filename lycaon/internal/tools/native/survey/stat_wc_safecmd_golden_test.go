package survey

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

func TestStatWcCapsComeFromSafecmd(t *testing.T) {
	if safecmd.StatMaxPaths != 50 || safecmd.StatCaps().ResultCount != safecmd.StatMaxPaths {
		t.Fatalf("stat caps = %d / %#v", safecmd.StatMaxPaths, safecmd.StatCaps())
	}
	if safecmd.WCMaxFiles != 50 || safecmd.WCMaxRecursiveFiles != 500 {
		t.Fatalf("wc caps = %d / %d", safecmd.WCMaxFiles, safecmd.WCMaxRecursiveFiles)
	}
}
