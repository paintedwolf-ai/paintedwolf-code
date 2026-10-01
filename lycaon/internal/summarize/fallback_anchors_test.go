package summarize

import "testing"

func TestPickPackFallbackAnchorsPrefersSymbolLineNotWindowHead(t *testing.T) {
	pack := ContextPack{
		Skeleton: []PackSymbol{
			{Path: "pkg/a.go", Kind: "func", Name: "Assemble", Line: 20},
			{Path: "pkg/a.go", Kind: "func", Name: "Fill", Line: 40},
		},
		Substance: []PackWindow{{
			Path: "pkg/a.go", StartLine: 18, EndLine: 22, Symbol: "Assemble",
			// The symbol follows a leading fragment.
			Body: "18: )\n19: {\n20: func Assemble() {\n21: \treturn\n22: }",
		}, {
			Path: "pkg/a.go", StartLine: 38, EndLine: 42, Symbol: "Fill",
			Body: "38: }\n39: \n40: func Fill() error {\n41: \treturn nil\n42: }",
		}},
	}
	got := pickPackAnchors(pack, DefaultCaps(), 0)
	if len(got) < 2 {
		t.Fatalf("anchors = %#v, want ≥2", got)
	}
	if got[0].Excerpt != "func Assemble() {" {
		t.Fatalf("anchor[0].Excerpt = %q, want symbol line (not window head)", got[0].Excerpt)
	}
	if got[1].Excerpt != "func Fill() error {" {
		t.Fatalf("anchor[1].Excerpt = %q", got[1].Excerpt)
	}
}

func TestPickPackAnchorsNeverInventSourceFromSymbolName(t *testing.T) {
	pack := ContextPack{
		Skeleton: []PackSymbol{{Path: "pkg/a.go", Kind: "func", Name: "OnlyName", Line: 10}},
		Substance: []PackWindow{{
			Path: "pkg/a.go", StartLine: 10, EndLine: 12, Symbol: "OnlyName",
			Body: "10: )\n11: }\n12: }",
		}},
	}
	got := pickPackAnchors(pack, DefaultCaps(), 0)
	if len(got) != 0 {
		t.Fatalf("anchors = %#v, want no invented source quote", got)
	}
}

func TestPickPackAnchorsSkipsDirMapAndRollupRows(t *testing.T) {
	// Synthetic rows have no source citation.
	pack := ContextPack{
		Skeleton: []PackSymbol{
			{Path: "pkg", Kind: "directory_map", Name: "pkg", Line: 1},
			{Path: "pkg/sub", Kind: KindDirectoryRollup, Name: "3 files · Go · top: Foo", Line: 1},
			{Path: "pkg/a.go", Kind: "func", Name: "Real", Line: 10},
		},
		Substance: []PackWindow{{
			Path: "pkg/a.go", StartLine: 10, EndLine: 11, Symbol: "Real",
			Body: "10: func Real() {\n11: }",
		}},
	}
	got := pickPackAnchors(pack, DefaultCaps(), 0)
	if len(got) != 1 {
		t.Fatalf("anchors = %#v, want only the real child-file symbol", got)
	}
	if got[0].Path != "pkg/a.go" || got[0].Excerpt != "func Real() {" {
		t.Fatalf("anchor = %#v, want the real func line", got[0])
	}
}

func TestPickPackAnchorsPreservesUnicodeSource(t *testing.T) {
	pack := ContextPack{Substance: []PackWindow{{Path: "guide.md", StartLine: 1, EndLine: 2, Body: "1: # 概要\n2: 文書の内容\n"}}}
	anchors := pickPackAnchors(pack, DefaultCaps(), 1)
	if len(anchors) != 1 || anchors[0].Excerpt != "# 概要" {
		t.Fatalf("unicode source anchors=%+v", anchors)
	}
}
