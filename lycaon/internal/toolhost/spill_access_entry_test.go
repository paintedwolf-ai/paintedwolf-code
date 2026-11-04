package toolhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/isolation"
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

func TestSpillChildConsumesThroughNativeReaderPipeline_ACSPILL01(t *testing.T) {
	f := newSpillFixture(t)
	converted := `"$(native_path -- ` + f.rel + `)"`
	for name, source := range map[string]string{
		"pipe logical":     "cat " + f.rel + " | /bin/cat",
		"pipe converted":   "cat " + converted + " | /bin/cat",
		"stdin redirect":   "/bin/cat < " + f.rel,
		"scratch copy":     "cat " + f.rel + " > @scratch/spill.txt; /bin/cat \"$TMPDIR/spill.txt\"",
		"variable quoted":  "p=" + converted + "; cat \"$p\" | /bin/cat",
		"from a subfolder": "mkdir sub; cd sub; cat " + f.rel + " | /bin/cat",
	} {
		t.Run(name, func(t *testing.T) {
			out := f.run(t, source)
			if out.Status != 0 || out.Stdout != spillBody {
				t.Fatalf("status=%d stdout=%q stderr=%q", out.Status, out.Stdout, out.Stderr)
			}
		})
	}
}

func TestSpillConversionPrintsTheHostPathAndGrantsNothing_ACSPILL01(t *testing.T) {
	f := newSpillFixture(t)
	out := f.run(t, "native_path -- "+f.rel)
	if out.Status != 0 || out.Stdout != f.abs+"\n" {
		t.Fatalf("converted status=%d stdout=%q stderr=%q", out.Status, out.Stdout, out.Stderr)
	}
	direct := f.run(t, `/bin/cat "$(native_path -- `+f.rel+`)"`)
	if direct.Stdout == spillBody {
		t.Fatalf("a confined child opened the converted spill's bytes: %+v", direct)
	}
	if relative := f.run(t, "/bin/cat "+f.rel); relative.Status == 0 {
		t.Fatalf("a child resolved the logical reference: %+v", relative)
	}
}

func TestSpillNativeReadersSeePlainBytes_ACSPILL02(t *testing.T) {
	f := newSpillFixture(t)
	for _, tc := range []struct{ source, want string }{
		{"cat " + f.rel, spillBody},
		{"cat " + f.quotedAbs(), spillBody},
		{"cat < " + f.rel, spillBody},
		{"head -n 1 " + f.rel, "alpha spill\n"},
		{"tail -n 1 " + f.rel, "gamma spill\n"},
		{"sed -n 2,2p " + f.rel, "beta spill\n"},
		{"grep beta " + f.rel, "beta spill\n"},
		{"rg beta " + f.rel, "beta spill\n"},
		{"wc -l < " + f.rel, "3\n"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			out := f.run(t, tc.source)
			if out.Status != 0 || out.Stdout != tc.want {
				t.Fatalf("status=%d stdout=%q stderr=%q", out.Status, out.Stdout, out.Stderr)
			}
		})
	}
}

// quotedAbs is the absolute spill path quoted for a program.
func (f *spillFixture) quotedAbs() string { return "'" + f.abs + "'" }

func TestSpillNamespacesResolveFromTheReadAllowlist_ACSPILL02(t *testing.T) {
	f := newSpillFixture(t)
	promote := filepath.Join(f.host, tooloutput.PromoteSpillDir, "job.json")
	testutil.FailErr(t, "promote dir", os.MkdirAll(filepath.Dir(promote), 0o700))
	testutil.FailErr(t, "promote body", os.WriteFile(promote, []byte("{}\n"), 0o600))
	out := f.run(t, "cat "+tooloutput.PromoteSpillDir+"/job.json")
	if out.Status != 0 || out.Stdout != "{}\n" {
		t.Fatalf("promote spill status=%d stdout=%q stderr=%q", out.Status, out.Stdout, out.Stderr)
	}
	// A sibling of the allowlisted names remains an ordinary relative path.
	sibling := filepath.Join(f.host, "evidence", "x.jsonl")
	testutil.FailErr(t, "sibling dir", os.MkdirAll(filepath.Dir(sibling), 0o700))
	testutil.FailErr(t, "sibling body", os.WriteFile(sibling, []byte("private"), 0o600))
	if out := f.run(t, "cat evidence/x.jsonl"); out.Status == 0 || out.Stdout != "" {
		t.Fatalf("unlisted host namespace resolved: %+v", out)
	}
}

func TestSpillReferenceLeavesAProjectEntryItsMeaning_ACSPILL02(t *testing.T) {
	f := newSpillFixture(t)
	project := filepath.Join(f.Dir, filepath.FromSlash(f.rel))
	testutil.FailErr(t, "project dir", os.MkdirAll(filepath.Dir(project), 0o700))
	testutil.FailErr(t, "project file", os.WriteFile(project, []byte("project\n"), 0o600))
	if out := f.run(t, "cat "+f.rel); out.Status != 0 || out.Stdout != "project\n" {
		t.Fatalf("read status=%d stdout=%q stderr=%q", out.Status, out.Stdout, out.Stderr)
	}
	if out := f.run(t, "native_path -- "+f.rel); out.Status != 0 || !strings.HasPrefix(out.Stdout, fspath.CanonicalPath(f.Dir)) && !strings.HasPrefix(out.Stdout, f.Dir) {
		t.Fatalf("conversion status=%d stdout=%q want under %q", out.Status, out.Stdout, f.Dir)
	}
	// Writes keep their project meaning whether or not a spill has the name.
	created := "tool-output/created.txt"
	testutil.FailErr(t, "seed host file of that name", os.WriteFile(filepath.Join(f.host, filepath.FromSlash(created)), []byte("host\n"), 0o600))
	if out := f.run(t, "printf new > "+created); out.Status != 0 {
		t.Fatalf("project write: %+v", out)
	}
	body, err := os.ReadFile(filepath.Join(f.Dir, filepath.FromSlash(created)))
	testutil.FailErr(t, "read project write", err)
	if string(body) != "new" {
		t.Fatalf("project write = %q", body)
	}
}

func TestSpillForbiddenPathsStayRefused_ACSPILL02(t *testing.T) {
	f := newSpillFixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	testutil.FailErr(t, "outside file", os.WriteFile(outside, []byte("private"), 0o600))
	testutil.FailErr(t, "outward link", os.Symlink(outside, filepath.Join(f.host, tooloutput.ToolOutputSpillDir, "link.txt")))
	unrelated := filepath.Join(f.host, "evidence.jsonl")
	testutil.FailErr(t, "unrelated host file", os.WriteFile(unrelated, []byte("private"), 0o600))
	otherProject := filepath.Join(f.config, "projects", "other", tooloutput.ToolOutputSpillDir, "x.txt")
	testutil.FailErr(t, "other project dir", os.MkdirAll(filepath.Dir(otherProject), 0o700))
	testutil.FailErr(t, "other project spill", os.WriteFile(otherProject, []byte("private"), 0o600))
	otherSession, err := scratch.New(f.config).Ensure("other-session")
	testutil.FailErr(t, "other session", err)
	otherOutput := filepath.Join(otherSession.Output, "program.stdout")
	testutil.FailErr(t, "other session output", os.WriteFile(otherOutput, []byte("private"), 0o600))
	for _, tc := range []struct{ name, operand, code string }{
		{"symlink escape", "tool-output/link.txt", "SURVEY_PATH_ESCAPE"},
		{"unrelated host file", "'" + unrelated + "'", isolation.CodeControlPlaneDenied},
		{"other project", "'" + otherProject + "'", isolation.CodeControlPlaneDenied},
		{"other session", "'" + otherOutput + "'", isolation.CodeControlPlaneDenied},
	} {
		for _, verb := range []string{"cat --", "native_path --"} {
			t.Run(tc.name+"/"+verb, func(t *testing.T) {
				out := f.run(t, verb+" "+tc.operand)
				if out.Status == 0 || out.Stdout != "" || out.Refusal == nil || out.Refusal.Code != tc.code {
					t.Fatalf("status=%d stdout=%q refusal=%+v want %s", out.Status, out.Stdout, out.Refusal, tc.code)
				}
			})
		}
	}
	// Traversal leaves the allowlist, so it reaches no host file.
	testutil.FailErr(t, "traversal target", os.WriteFile(filepath.Join(f.host, "secret"), []byte("private"), 0o600))
	if out := f.run(t, "cat tool-output/../secret"); out.Status == 0 || out.Stdout != "" {
		t.Fatalf("traversal: %+v", out)
	}
}

func TestSpillExpiredReferenceFailsCleanly_ACSPILL02(t *testing.T) {
	f := newSpillFixture(t)
	testutil.FailErr(t, "expire spill", os.Remove(f.abs))
	for _, source := range []string{"cat " + f.rel, "native_path -- " + f.rel, "cat " + f.rel + " | /bin/cat", `cat "$(native_path -- ` + f.rel + `)"`} {
		t.Run(source, func(t *testing.T) {
			out := f.run(t, source)
			if out.Status == 0 || out.Stdout != "" {
				t.Fatalf("expired spill: %+v", out)
			}
		})
	}
}

func TestSpillConversionHoldsTheBodyPastReclamationGrace_ACSPILL02(t *testing.T) {
	f := newSpillFixture(t)
	aged := time.Now().Add(-time.Hour)
	cutoff := time.Now().Add(-time.Minute)
	testutil.FailErr(t, "age spill", os.Chtimes(f.abs, aged, aged))
	if out := f.run(t, "native_path -- "+f.rel); out.Status != 0 {
		t.Fatalf("convert: %+v", out)
	}
	removed, err := tooloutput.RemoveSpillBefore(f.host, f.rel, cutoff)
	testutil.FailErr(t, "reclaim", err)
	if removed {
		t.Fatal("reclamation removed a just-converted spill")
	}
	if out := f.run(t, "cat "+f.rel+" | /bin/cat"); out.Status != 0 || out.Stdout != spillBody {
		t.Fatalf("read after reclaim attempt: %+v", out)
	}
	// An ordinary read takes no hold; only an actual open pins bytes for it.
	testutil.FailErr(t, "age spill again", os.Chtimes(f.abs, aged, aged))
	if out := f.run(t, "cat "+f.rel); out.Status != 0 {
		t.Fatalf("read: %+v", out)
	}
	removed, err = tooloutput.RemoveSpillBefore(f.host, f.rel, cutoff)
	testutil.FailErr(t, "reclaim unconverted", err)
	if !removed {
		t.Fatal("a read extended retention")
	}
}
