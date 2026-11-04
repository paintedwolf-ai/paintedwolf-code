package toolhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

const spillBody = "alpha spill\nbeta spill\ngamma spill\n"

type spillFixture struct {
	*programtest.Fixture
	config string
	host   string
	rel    string
	abs    string
}

// newSpillFixture retains spillBody for a project whose host data directory has
// spaces, under the state tree confinement floors for child reads.
func newSpillFixture(t *testing.T) *spillFixture {
	t.Helper()
	config := filepath.Join(fspath.CanonicalPath(t.TempDir()), "host config")
	t.Setenv("LYCAON_CONFIG_DIR", config)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	host := filepath.Join(config, "projects", "project one")
	testutil.FailErr(t, "create host data", os.MkdirAll(host, 0o700))
	rel := tooloutput.SpillWholeRaw(context.Background(), host, tooloutput.Screened(spillBody), 0).SpillPath
	if rel == "" {
		t.Fatal("spill was not retained")
	}
	f := newOutputDeliveryFixture(t)
	f.Context = func(_ string, tc *tools.ToolContext) { tc.Host.HostDataDir = host }
	return &spillFixture{Fixture: f, config: config, host: host, rel: rel, abs: filepath.Join(host, filepath.FromSlash(rel))}
}

func (f *spillFixture) run(t *testing.T, source string) command.ProgramOutcome {
	t.Helper()
	raw, err := f.Call(t, t.Context(), source)
	testutil.FailErr(t, "run "+source, err)
	return decodeProgramOutcome(t, raw)
}

func TestSpillProbe(t *testing.T) {
	f := newSpillFixture(t)
	q := "'" + f.abs + "'"
	for _, src := range []string{
		"cat " + f.rel, "head -n 1 " + f.rel, "tail -n 1 " + f.rel, "sed -n 2,2p " + f.rel, "wc -c " + f.rel,
		"grep beta " + f.rel, "rg beta " + f.rel, "awk 'NR==2' " + f.rel, "sort " + f.rel, "sha256sum " + f.rel,
		"cp -- " + f.rel + " @scratch/c.txt; cat @scratch/c.txt", "cat < " + f.rel, "test -f " + f.rel, "stat -- " + f.rel, "realpath " + f.rel,
		"wc -l < " + f.rel, "diff " + f.rel + " " + f.rel, "cut -c1-3 " + f.rel, "tr a b < " + f.rel, "uniq " + f.rel, "nl " + f.rel, "xxd " + f.rel, "od -c " + f.rel, "strings " + f.rel,
		"native_path -- " + f.rel,
		"p=\"$(native_path -- " + f.rel + ")\"; cat \"$p\"",
		"p=\"$(native_path -- " + f.rel + ")\"; /bin/cat \"$p\"",
		"cat " + f.rel + " | /bin/cat",
		"cat " + f.rel + " > @scratch/d.txt; /bin/cat \"$TMPDIR/d.txt\"",
		"/bin/cat " + q,
	} {
		raw, err := f.Call(t, t.Context(), src)
		if err != nil {
			t.Logf("SRC %s\n  ERR %v", src, err)
			continue
		}
		o := decodeProgramOutcome(t, raw)
		t.Logf("SRC %s\n  status=%d stdout=%q stderr=%.120q", strings.ReplaceAll(src, f.abs, "<abs>"), o.Status, o.Stdout, o.Stderr)
	}
	// grants
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Host.HostDataDir = f.host
		tc.Files.SessionReadPaths = []string{f.abs}
	}
	o := f.run(t, "/bin/cat "+q)
	t.Logf("GRANT status=%d stdout=%q stderr=%.120q", o.Status, o.Stdout, o.Stderr)
	f.Context = func(_ string, tc *tools.ToolContext) { tc.Host.HostDataDir = f.host }
	outside := filepath.Join(t.TempDir(), "outside")
	testutil.FailErr(t, "w", os.WriteFile(outside, []byte("private"), 0o600))
	link := filepath.Join(f.host, "tool-output", "link.txt")
	testutil.FailErr(t, "l", os.Symlink(outside, link))
	for _, src := range []string{"cat tool-output/link.txt", "native_path -- tool-output/link.txt", "cat '" + link + "'", "native_path -- tool-output/gone.txt", "cat tool-output/gone.txt", "native_path -- tool-output/../evidence", "cat tool-output/../evidence"} {
		raw, err := f.Call(t, t.Context(), src)
		if err != nil {
			t.Logf("SRC %s\n  ERR %v", src, err)
			continue
		}
		o := decodeProgramOutcome(t, raw)
		code := ""
		if o.Refusal != nil {
			code = o.Refusal.Code
		}
		t.Logf("SRC %s\n  status=%d stdout=%q stderr=%.120q code=%s", src, o.Status, o.Stdout, o.Stderr, code)
	}
	_ = json.Valid
	_ = scratch.New
	_ = time.Now
	t.Fatal("probe")
}
