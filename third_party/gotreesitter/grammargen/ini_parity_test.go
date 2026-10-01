package grammargen

import "testing"

func TestIniImportedTextTokenAllowsZeroWidth(t *testing.T) {
	genLang, _ := loadImportedParityLanguages(t, "ini")
	var textSyms []int
	var zeroWidthTextSyms []int
	for i, name := range genLang.SymbolNames {
		if name == "text" {
			textSyms = append(textSyms, i)
			if i < len(genLang.ZeroWidthTokens) && genLang.ZeroWidthTokens[i] {
				zeroWidthTextSyms = append(zeroWidthTextSyms, i)
			}
		}
	}
	if len(textSyms) == 0 {
		t.Fatalf("text symbol not found in imported INI grammar")
	}
	if len(zeroWidthTextSyms) == 0 {
		t.Fatalf("no text symbol is marked zero-width-capable; text symbols=%v", textSyms)
	}
}

func TestIniEmptyCommentTextDeepParity(t *testing.T) {
	assertImportedDeepParityCases(t, "ini", []struct {
		name string
		src  string
	}{
		{name: "empty_comment_text", src: "#      \n;\n#\n"},
	})
}
