package contract

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
)

func TestKickTemplatesMatchBehaviorContract(t *testing.T) {
	t.Parallel()
	catalogfixture.AssertNoKickConstFiles(t)
	reg := catalogfixture.LoadInformBindings(t)
	indexWorkflowInjects(t, reg)

	declared := map[string]struct {
		forbidden []string
		required  []string
		render    string
	}{}
	collect := func(b *anchor.Binding) {
		if b == nil || !b.IsInform() || !strings.HasPrefix(b.Render, "coordinator-") {
			return
		}
		id := catalogfixture.BindingShortID(b.Render)
		declared[id] = struct {
			forbidden []string
			required  []string
			render    string
		}{b.Invariants.Forbidden, b.Invariants.Required, b.Render}
	}
	for _, b := range reg.AllInform() {
		collect(b)
	}
	for _, b := range reg.BindingsOn(catalogfixture.MustParseAnchor(t, "phase.entered")) {
		collect(b)
	}

	onDisk := map[string]string{}
	for _, dir := range catalogfixture.StockGuidanceDirs(t) {
		entries, err := dir.List()
		testutil.FailErr(t, "read kicks dir", err)
		for _, ent := range entries {
			name := ent.Name()
			if ent.IsDir() || !strings.HasPrefix(name, "coordinator-") || !strings.HasSuffix(name, ".md") {
				continue
			}
			id := strings.TrimSuffix(strings.TrimPrefix(name, "coordinator-"), ".md")
			raw, err := dir.Join(name).Read()
			testutil.FailErr(t, "read "+name, err)
			onDisk[id] = string(raw)
		}
	}

	var uncovered []string
	for id := range onDisk {
		if _, ok := declared[id]; !ok {
			uncovered = append(uncovered, id)
		}
	}
	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Fatalf("coordinator templates with no inform Binding: %v", uncovered)
	}
	var missing []string
	for id, d := range declared {
		if _, ok := onDisk[id]; !ok {
			missing = append(missing, id+" ("+d.render+")")
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("Bindings declare missing coordinator templates: %v", missing)
	}

	for id, body := range onDisk {
		d := declared[id]
		for _, must := range d.required {
			if must == "" {
				continue
			}
			if !strings.Contains(body, must) {
				t.Errorf("%s: required %q missing", id, must)
			}
		}
		for _, forbid := range d.forbidden {
			if forbid == "" {
				continue
			}
			if strings.Contains(body, forbid) {
				t.Errorf("%s: forbidden %q present", id, forbid)
			}
		}
	}
}
