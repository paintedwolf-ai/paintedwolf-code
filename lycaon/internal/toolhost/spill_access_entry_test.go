package toolhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestSpillProbe(t *testing.T) {
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "home", err)
	base, err := os.MkdirTemp(home, ".pw spill probe ")
	testutil.FailErr(t, "base", err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base = fspath.CanonicalPath(base)
	t.Setenv("LYCAON_CONFIG_DIR", base)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	host := filepath.Join(base, "projects", "p one")
	testutil.FailErr(t, "mkdir", os.MkdirAll(host, 0o700))
	f := newOutputDeliveryFixture(t)
	f.Context = func(_ string, tc *tools.ToolContext) { tc.Host.HostDataDir = host }
	body := "alpha spill\nbeta spill\n"
	out := tooloutput.SpillWholeRaw(context.Background(), host, tooloutput.Screened(body), 0)
	rel := out.SpillPath
	abs := filepath.Join(host, rel)
	t.Logf("rel=%s abs=%s", rel, abs)
	for _, src := range []string{
		"sed -n 1,1p " + rel,
		"cat " + rel,
		"native_path -- " + rel,
		"native_path -- '" + abs + "'",
		"cat '" + abs + "'",
		"sed -n 2,2p '" + abs + "'",
		"cat \"$(native_path -- '" + abs + "')\" | /bin/cat",
		"/bin/cat '" + abs + "'",
		"/bin/cat " + rel,
		"/bin/cat '" + abs + "' | /usr/bin/wc -c",
		"cat '" + abs + "' > @scratch/copy.txt; /bin/cat \"$TMPDIR/copy.txt\"",
		"native_path -- '" + filepath.Join(host, "evidence.jsonl") + "'",
		"native_path -- tool-output/../../secret",
	} {
		raw, err := f.Call(t, t.Context(), src)
		if err != nil {
			t.Logf("SRC %s\n  ERR %v", src, err)
			continue
		}
		o := decodeProgramOutcome(t, raw)
		t.Logf("SRC %s\n  status=%d stdout=%q stderr=%q refusal=%+v", src, o.Status, trunc(o.Stdout), trunc(o.Stderr), o.Refusal)
	}
	t.Fatal("probe output")
}

func trunc(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return strings.ToValidUTF8(s, "?")
}
