package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestGrepInvalidRegexProgramStatus(t *testing.T) {
	f := toolfixture.NewProgramFixture(t)
	out, err := f.Invoke(t, "rg '[unclosed' .", tools.ToolContext{
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}}, ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	contractcheck.FailErr(t, "invoke invalid regex program", err)
	if out.Status != 2 || out.Refusal != nil || out.Stdout != "" || out.Stderr == "" {
		t.Fatalf("invalid regex lost its ordinary command failure: %+v", out)
	}
}

func TestRipgrepAlternationMatchesByDefault(t *testing.T) {
	root := t.TempDir()
	contractcheck.FailErr(t, "write alternation fixture", os.WriteFile(filepath.Join(root, "secrets.txt"), []byte("token=1\n"), 0o644))
	f := toolfixture.NewProgramFixture(t)
	out, err := f.Invoke(t, "rg 'password|secret|token' .", tools.ToolContext{
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}}, ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "explore_readonly"},
	})
	contractcheck.FailErr(t, "invoke native RE2 alternation", err)
	if out.Status != 0 || out.Refusal != nil || !strings.Contains(out.Stdout, "token=1") {
		t.Fatalf("alternation should match natively: %+v", out)
	}
}

func TestGrepSchemaHasRegexFields(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir failed", err)
	entry, ok := schemas.Tools["grep"]
	if !ok {
		t.Fatal("grep missing from tools/schemas")
	}
	props, _ := entry.Schema["properties"].(map[string]any)
	if props == nil {
		t.Fatal("grep schema missing properties")
	}
	for _, key := range []string{"case_insensitive", "max_matches", "context_lines"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("grep schema missing property %q", key)
		}
	}
	// Text search is RE2 unconditionally: a literal/regex mode flag is the one
	// arg an agent can set wrong and get a silent empty result from.
	if _, ok := props["regex"]; ok {
		t.Fatal("grep schema must not offer a regex mode flag")
	}
}
