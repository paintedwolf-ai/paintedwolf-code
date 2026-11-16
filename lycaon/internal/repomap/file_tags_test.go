package repomap_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/repomap"
)

func TestTagsFromBytesCSSSelectors(t *testing.T) {
	src := []byte(":root {\n  --bg: #fff;\n}\nbody {\n  margin: 0;\n}\n#board {\n  display: grid;\n}\n.cell {\n  width: 48px;\n}\n.cell:hover,\n.cell:not(.occupied) {\n  background: #eee;\n}\nbutton[disabled] {\n  opacity: 0.5;\n}\n@keyframes pulse {\n  from { opacity: 1; }\n  to { opacity: 0.4; }\n}\n")
	outcome := repomap.TagsFromBytes(context.Background(), "style.css", src)
	if outcome.Language != "css" {
		t.Fatalf("language = %q, want css", outcome.Language)
	}
	type item struct {
		kind, name string
		line       int
	}
	want := []item{
		{"rule", ":root", 1},
		{"rule", "body", 4},
		{"id", "#board", 7},
		{"class", ".cell", 10},
		{"rule", ".cell:hover, .cell:not(.occupied)", 13},
		{"rule", "button[disabled]", 17},
		{"function", "pulse", 20},
	}
	if len(outcome.Tags) != len(want) {
		t.Fatalf("tags = %+v, want %+v", outcome.Tags, want)
	}
	for i, w := range want {
		got := outcome.Tags[i]
		if got.Kind != w.kind || got.Name != w.name || got.Line != w.line {
			t.Fatalf("tag[%d] = %+v, want %+v", i, got, w)
		}
	}
}
