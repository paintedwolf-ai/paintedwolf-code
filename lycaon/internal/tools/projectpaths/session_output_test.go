package projectpaths_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestSessionOutputReadsItsOwnHostFolderAndRefusesNativeWrites(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	folders := scratch.New(cfg)
	own, err := folders.Ensure("own")
	testutil.FailErr(t, "own folders", err)
	other, err := folders.Ensure("other")
	testutil.FailErr(t, "other folders", err)
	tc, _, rec := scratchToolContext(t)
	tc.Host.SessionScratchDir, tc.Host.SessionOutputDir = own.Scratch, own.Output
	testutil.FailErr(t, "publish own copy", scratch.PublishCommandOutput(own.Output, "program", "stdout", strings.NewReader("one\ntwo\n")))
	testutil.FailErr(t, "publish other copy", scratch.PublishCommandOutput(other.Output, "program", "stdout", strings.NewReader("private\n")))
	reads := projectpaths.NewReadSession(nil, tc)
	defer reads.Close()
	for _, path := range []string{"@output/program.stdout", "@OUTPUT/program.stdout", filepath.Join(own.Output, "program.stdout")} {
		resolved, err := reads.Resolve(t.Context(), path)
		testutil.FailErr(t, "resolve output", err)
		if !resolved.ToolOutput || resolved.Root.ID != projectroot.VirtualOutputLabel || resolved.DisplayPath != "@output/program.stdout" {
			t.Fatalf("resolved=%+v", resolved)
		}
		file, err := reads.Open(t.Context(), resolved)
		testutil.FailErr(t, "open output", err)
		bytes, err := io.ReadAll(file)
		file.Close()
		testutil.FailErr(t, "read copy", err)
		if string(bytes) != "one\ntwo\n" {
			t.Fatalf("copy=%q", bytes)
		}
		if _, err := projectpaths.ResolveWrite(t.Context(), nil, tc, path); err == nil {
			t.Fatalf("output became a native write root: %s", path)
		}
	}
	if len(rec.mutations) != 0 {
		t.Fatalf("output read recorded mutations=%v", rec.mutations)
	}
	if _, err := reads.Resolve(t.Context(), filepath.Join(other.Output, "program.stdout")); err == nil {
		t.Fatal("read another session's output")
	}
	outside := t.TempDir()
	testutil.FailErr(t, "outside file", os.WriteFile(filepath.Join(outside, "private"), []byte("outside"), 0600))
	testutil.FailErr(t, "outward link", os.Symlink(filepath.Join(outside, "private"), filepath.Join(own.Output, "link")))
	if _, err := reads.Resolve(t.Context(), "@output/link"); err == nil {
		t.Fatal("output address escaped through a link")
	}
	tc.Host.SessionOutputDir = ""
	_, err = projectpaths.ResolveRead(t.Context(), nil, tc, "@output/program.stdout")
	requireReject(t, err, tools.SessionOutputUnavailableCode)
}
