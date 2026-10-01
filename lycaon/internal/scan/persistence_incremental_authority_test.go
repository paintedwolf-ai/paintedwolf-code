package scan

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestIncrementalReuseIdentityIncludesAncestor(t *testing.T) {
	scan := authorityScan("scan", "assessment", t.TempDir(), "snapshot-2", "sast", api.ScanTargetPaths, nil)
	scan.TargetPaths = []string{"a.go"}
	manual, err := scanReuseKey(scan, "")
	testutil.FailErr(t, "manual identity", err)
	derived, err := scanReuseKey(scan, "snapshot-1")
	testutil.FailErr(t, "derived identity", err)
	if manual == derived {
		t.Fatal("caller target and proven generation share reuse identity")
	}
}
