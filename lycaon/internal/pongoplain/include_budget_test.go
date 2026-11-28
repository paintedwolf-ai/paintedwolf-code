package pongoplain

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIncludeInAFullLoopFitsTheStepBudgetWithAFullContext(t *testing.T) {
	set, err := NewSet("include-loop-budget", memTemplateLoader{
		"a.md": `{% for a in xs %}{% include "b.md" %}{% endfor %}`,
		"b.md": `{% if flag %}x{% endif %}`,
	}, Composed)
	testutil.FailErr(t, "NewSet", err)
	tpl, err := set.FromFile("a.md")
	testutil.FailErr(t, "FromFile", err)
	data := map[string]any{"xs": make([]int, MaxCollectionItems), "flag": true}
	for i := len(data); i < MaxContextEntries; i++ {
		data[fmt.Sprintf("fact_%d", i)] = i
	}

	out, err := Execute(t.Context(), tpl, data)
	testutil.FailErr(t, "execute", err)
	if out != strings.Repeat("x", MaxCollectionItems) {
		t.Fatalf("output length = %d, want %d", len(out), MaxCollectionItems)
	}
}

func TestIncludeCountStaysBounded(t *testing.T) {
	set, err := NewSet("include-count-bound", memTemplateLoader{
		"a.md": `{% for a in xs %}{% include "b.md" %}{% endfor %}{% include "b.md" %}`,
		"b.md": `x`,
	}, Composed)
	testutil.FailErr(t, "NewSet", err)
	tpl, err := set.FromFile("a.md")
	testutil.FailErr(t, "FromFile", err)
	if _, err := Execute(t.Context(), tpl, map[string]any{"xs": make([]int, MaxCollectionItems)}); !errors.Is(err, ErrComposition) {
		t.Fatalf("include %d times: err = %v, want ErrComposition", MaxCompositionFiles+1, err)
	}
}
