package oar

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestImportInvariantSubsetRoundTrip(t *testing.T) {
	src := `
raise "Forbid command" if:
    (call: ToolCall)
    call.name == "command"
`
	imp, err := ImportInvariantSubset(src)
	testutil.FailErr(t, "import", err)
	if imp.ID != "IMPORTED_FORBID_COMMAND" {
		t.Fatalf("id=%s", imp.ID)
	}
	if imp.When != `tool == "command"` {
		t.Fatalf("when=%q", imp.When)
	}

	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)
	rule := imp.ToRule()

	p := NewGuardPipeline(NewRuleSet([]*Rule{rule}), l, NewCounterStore())

	gc := NewGuardContext()
	gc.Tool = "command"
	res, err := p.Evaluate(context.Background(), StagePreInvoke, gc)
	testutil.FailErr(t, "eval command", err)
	if res.Decision == nil || res.Decision.Effect != EffectBlock {
		t.Fatalf("expected block on command, got %#v", res.Decision)
	}

	gc2 := NewGuardContext()
	gc2.Tool = "read"
	res, err = p.Evaluate(context.Background(), StagePreInvoke, gc2)
	testutil.FailErr(t, "eval read", err)
	if res.Decision != nil {
		t.Fatalf("read should pass, got %#v", res.Decision)
	}
}

func TestImportInvariantFlow(t *testing.T) {
	src := `
raise "Read then write" if:
    (a: ToolCall) -> (b: ToolCall)
    a.name == "read"
    b.name == "write"
`
	imp, err := ImportInvariantSubset(src)
	testutil.FailErr(t, "import", err)
	if len(imp.Flow) != 2 || imp.Flow[0] != "read" || imp.Flow[1] != "write" {
		t.Fatalf("flow=%v", imp.Flow)
	}

	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)
	rule := imp.ToRule()

	p := NewGuardPipeline(NewRuleSet([]*Rule{rule}), l, NewCounterStore())

	gc := NewGuardContext()
	gc.RecentToolNames = []string{"read", "write"}
	res, err := p.Evaluate(context.Background(), StagePreInvoke, gc)
	testutil.FailErr(t, "eval flow", err)
	if res.Decision == nil {
		t.Fatalf("expected fire, got %#v trace=%#v", res.Decision, res.Trace)
	}

	gc.RecentToolNames = []string{"read"}
	res, err = p.Evaluate(context.Background(), StagePreInvoke, gc)
	testutil.FailErr(t, "eval incomplete", err)
	if res.Decision != nil {
		t.Fatalf("incomplete flow should pass, got %#v", res.Decision)
	}
}

func TestImportInvariantSampleFiles(t *testing.T) {
	ensureCatalog(t)
	root := testutil.CheckoutRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "oar", "testdata", "invariant")
	ents, err := os.ReadDir(dir)
	testutil.FailErr(t, "readdir", err)
	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)
	n := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".inv") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		testutil.FailErr(t, "read "+e.Name(), err)
		imp, err := ImportInvariantSubset(string(raw))
		testutil.FailErr(t, "import "+e.Name(), err)
		yamlBytes, err := imp.ToHintYAML()
		testutil.FailErr(t, "yaml "+e.Name(), err)
		tmp := t.TempDir()
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmp, imp.ID+".yaml"), yamlBytes, 0o600))
		rs, err := l.LoadDirTier(extpacks.OnDisk(tmp), TierPack, "import:invariant")
		testutil.FailErr(t, "load "+e.Name(), err)
		if _, ok := rs.Get(imp.ID); !ok {
			t.Fatalf("%s: rule %s missing after load", e.Name(), imp.ID)
		}
		n++
	}
	if n == 0 {
		t.Fatal("expected sample .inv files")
	}
}
