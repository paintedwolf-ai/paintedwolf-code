package projectpaths_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestResolveReadAllowsHostDataDir(t *testing.T) {
	ws := t.TempDir()
	host := t.TempDir()
	spill := filepath.Join(host, "tool-output", "a.txt")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(spill), 0o700))
	testutil.FailErr(t, "write", os.WriteFile(spill, []byte("full"), 0o600))

	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Path: ws, IsPrimary: true}},
		ActiveRootID: "r1",
		HostDataDir:  host,
		Agent:        toolprofiles.DefaultToolProfileID,
	}
	// Absolute spill leftovers resolve (durable seatbelt); DisplayPath is relative.
	resolved, err := projectpaths.ResolveRead(context.Background(), nil, tctx, spill)
	testutil.FailErr(t, "ResolveRead host spill abs", err)
	if resolved.Abs != filepath.Clean(spill) {
		t.Fatalf("abs=%q want %q", resolved.Abs, spill)
	}
	if resolved.DisplayPath != "tool-output/a.txt" {
		t.Fatalf("display=%q want tool-output/a.txt", resolved.DisplayPath)
	}
	if !resolved.ToolOutput || !resolved.External {
		t.Fatal("host observation did not retain its recovery classification")
	}

	// Wire form: host-data-relative virtual path.
	resolvedRel, err := projectpaths.ResolveRead(context.Background(), nil, tctx, "tool-output/a.txt")
	testutil.FailErr(t, "ResolveRead host spill rel", err)
	if resolvedRel.Abs != filepath.Clean(spill) {
		t.Fatalf("rel abs=%q want %q", resolvedRel.Abs, spill)
	}
	if resolvedRel.DisplayPath != "tool-output/a.txt" {
		t.Fatalf("rel display=%q", resolvedRel.DisplayPath)
	}
}

func TestResolveReadMarksBlobstoreManagedDirsCompressed(t *testing.T) {
	ws := t.TempDir()
	host := t.TempDir()
	for _, rel := range []string{
		filepath.Join("tool-output", "a.txt"),
		filepath.Join("prompt-attachments", "abc123", "notes.txt"),
		filepath.Join("promote-spills", "job.json"),
	} {
		abs := filepath.Join(host, rel)
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o700))
		testutil.FailErr(t, "write", os.WriteFile(abs, []byte("x"), 0o600))
	}

	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Path: ws, IsPrimary: true}},
		ActiveRootID: "r1",
		HostDataDir:  host,
		Agent:        toolprofiles.DefaultToolProfileID,
	}

	cases := []struct {
		path string
		want bool
	}{
		{"tool-output/a.txt", true},
		{"prompt-attachments/abc123/notes.txt", true},
		{"promote-spills/job.json", false},
	}
	for _, c := range cases {
		resolved, err := projectpaths.ResolveRead(context.Background(), nil, tctx, c.path)
		testutil.FailErr(t, "ResolveRead "+c.path, err)
		if resolved.Compressed != c.want {
			t.Fatalf("ResolveRead(%q).Compressed = %v want %v", c.path, resolved.Compressed, c.want)
		}
	}
}

func TestResolveReadRejectsNonSpillUnderHostDataDir(t *testing.T) {
	ws := t.TempDir()
	host := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", host)
	evidence := filepath.Join(host, "evidence", "x.jsonl")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(evidence), 0o700))
	testutil.FailErr(t, "write", os.WriteFile(evidence, []byte("{}"), 0o600))

	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Path: ws, IsPrimary: true}},
		ActiveRootID: "r1",
		HostDataDir:  host,
		Agent:        toolprofiles.DefaultToolProfileID,
	}
	_, err := projectpaths.ResolveRead(context.Background(), nil, tctx, evidence)
	if err == nil {
		t.Fatal("expected reject for abs HostDataDir path outside spill prefixes")
	}
}

func TestResolveReadRejectsAbsoluteOutsideHostAndWorkspace(t *testing.T) {
	ws := t.TempDir()
	host := t.TempDir()
	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Path: ws, IsPrimary: true}},
		ActiveRootID: "r1",
		HostDataDir:  host,
		Agent:        toolprofiles.DefaultToolProfileID,
	}
	outside := filepath.Join(filepath.VolumeName(ws)+string(filepath.Separator), "unattached", t.Name(), "secret.txt")
	_, err := projectpaths.ResolveRead(context.Background(), nil, tctx, outside)
	if err == nil {
		t.Fatal("expected reject for absolute path outside host data and workspace")
	}
}

func TestIsAgentWireSpillRelRejectsEscapes(t *testing.T) {
	if tooloutput.IsAgentWireSpillRel("tool-output/../credential-vault.age") {
		t.Fatal("raw .. must not count as wire spill")
	}
	if tooloutput.IsAgentWireSpillRel("../tool-output/a.txt") {
		t.Fatal("leading .. must not count as wire spill")
	}
	if !tooloutput.IsAgentWireSpillRel("tool-output/a.txt") {
		t.Fatal("expected allowlisted tool-output path")
	}
}

func TestResolveMisplacedSpillFindsTheReferencedSpill(t *testing.T) {
	host := t.TempDir()
	rel := tooloutput.ToolOutputSpillRelPath("observation")
	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Path: t.TempDir(), IsPrimary: true}},
		ActiveRootID: "r1",
		HostDataDir:  host,
		Agent:        toolprofiles.DefaultToolProfileID,
	}
	resolved, ok := projectpaths.ResolveMisplacedSpill(tctx, "@scratch/"+rel)
	if !ok || resolved.DisplayPath != rel || resolved.Abs != filepath.Join(host, filepath.FromSlash(rel)) {
		t.Fatalf("resolved = %+v ok=%v", resolved, ok)
	}
	if _, ok := projectpaths.ResolveMisplacedSpill(tctx, rel); ok {
		t.Fatal("a path that already names the spill is not misplaced")
	}
	if _, ok := projectpaths.ResolveMisplacedSpill(tctx, "@scratch/notes/tool-output/short.txt"); ok {
		t.Fatal("only an exact content-addressed reference resolves")
	}
}
