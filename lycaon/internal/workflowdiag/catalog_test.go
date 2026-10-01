package workflowdiag_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflowdiag"
)

func TestAllCodesBijectionWithCatalog(t *testing.T) {
	cat, err := workflowdiag.Load()
	testutil.FailErr(t, "Load", err)

	want := map[workflowdiag.Code]struct{}{}
	for _, c := range workflowdiag.AllCodes() {
		if _, dup := want[c]; dup {
			t.Fatalf("duplicate AllCodes entry %q", c)
		}
		want[c] = struct{}{}
	}
	got := cat.Codes()
	for _, c := range got {
		if !cat.Has(c) {
			t.Fatalf("Codes() returned %q but Has is false", c)
		}
		if _, ok := want[c]; !ok {
			t.Fatalf("catalog has code %q not in AllCodes", c)
		}
		delete(want, c)
	}
	if len(want) > 0 {
		var missing []string
		for c := range want {
			missing = append(missing, string(c))
		}
		sort.Strings(missing)
		t.Fatalf("AllCodes missing from catalog: %v", missing)
	}
}

func TestEmitRendersTemplates(t *testing.T) {
	cat, err := workflowdiag.Load()
	testutil.FailErr(t, "Load", err)
	d := cat.Emit(workflowdiag.MustCode("unknown_agent"), "allowed_agents[0]", map[string]any{
		"agent": "nope",
	})
	if d.Code != string(workflowdiag.MustCode("unknown_agent")) {
		t.Fatalf("code = %q", d.Code)
	}
	if d.Field != "allowed_agents[0]" {
		t.Fatalf("field = %q", d.Field)
	}
	if d.Message == "" || d.Replacement == "" {
		t.Fatalf("message/replacement empty: %+v", d)
	}
	if !strings.Contains(d.Message, "nope") {
		t.Fatalf("message missing agent: %q", d.Message)
	}
}
