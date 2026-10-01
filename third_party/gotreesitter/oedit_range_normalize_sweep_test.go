package gotreesitter_test

// Byte-sweep differential for campaign O(edit) range-limited result
// normalization (spec.campaign.oedit). For every insert / delete / replace edit
// at every byte position of a multi-item source, the range-limited incremental
// parse must produce a tree IDENTICAL to the full-walk incremental parse -- and,
// where the full walk matches a fresh parse, identical to fresh too -- at every
// site where the fresh parse is genuinely clean (full-span, zero
// IsError/IsMissing).
//
// Identity is checked with oeditSerialize, NOT Node.SExpr: the three
// range-limited Go normalizers mutate exactly the things SExpr hides -- node
// byte spans (endByte adjustments) and anonymous children (dropped `;`). The
// serializer therefore records every node's type, [startByte-endByte], named vs
// anonymous, and missing flag, and walks ALL children (anonymous included).
//
// This proves the range-limited Go walk is sound (reused siblings skipped ==
// re-walked) and that the shared plumbing is neutral for the non-range-limited
// normalizer-bearing languages (scala/php/c_sharp) and the reference language
// (css).

import (
	"fmt"
	"strings"
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

type oeditSweepCase struct {
	name string
	lang *gts.Language
	src  []byte
}

func oeditSweepCases() []oeditSweepCase {
	goSrc := "package p\n\n" +
		"func A(a int) int {\n\tx := a + 1\n\treturn x\n}\n\n" +
		"func B(b int) int {\n\ty := b * 2\n\treturn y\n}\n\n" +
		"func C(c int) int {\n\tif c > 0 {\n\t\treturn c\n\t}\n\treturn 0\n}\n\n" +
		"type T struct {\n\tF int\n\tG string\n}\n\n" +
		"var V = 3\n"

	// Adversarial Go sources targeting the exact mutation class of the three
	// range-limited normalizers: auto-semicolon container drops, statement-list
	// and case sibling boundaries, and statement-list trailing extras (comments).
	goSwitch := "package p\n\n" +
		"func S(x int) int {\n" +
		"\tswitch x {\n" +
		"\tcase 1:\n\t\treturn 10 // one\n" +
		"\tcase 2:\n\t\treturn 20\n" +
		"\tdefault:\n\t\treturn 0\n" +
		"\t}\n}\n\n" +
		"func T(y int) int {\n\treturn y\n}\n"

	goComments := "package p\n\n" +
		"// leading\nfunc A() {\n" +
		"\ta := 1 // trailing a\n" +
		"\t// between\n" +
		"\tb := 2\n" +
		"\treturn\n}\n\n" +
		"/* block */\nfunc B() {}\n"

	goDecls := "package p\n\n" +
		"import (\n\t\"fmt\"\n\t\"os\"\n)\n\n" +
		"const (\n\tA = 1\n\tB = 2\n)\n\n" +
		"var (\n\tx = 1\n\ty = 2\n)\n\n" +
		"type (\n\tI int\n\tS string\n)\n\n" +
		"func F() { fmt.Println(os.Args) }\n"

	goSelect := "package p\n\n" +
		"func R(c chan int) {\n" +
		"\tselect {\n" +
		"\tcase v := <-c:\n\t\t_ = v\n" +
		"\tdefault:\n" +
		"\t}\n}\n\n" +
		"func Q() {}\n"

	scalaSrc := "object M {\n" +
		"  def a(x: Int): Int = x + 1\n" +
		"  def b(y: Int): Int = y * 2\n" +
		"  val c = 3\n" +
		"}\n\n" +
		"class K(n: Int) {\n" +
		"  def m: Int = n\n" +
		"}\n"

	phpSrc := "<?php\n" +
		"function a($x) {\n  return $x + 1;\n}\n\n" +
		"function b($y) {\n  return $y * 2;\n}\n\n" +
		"class K {\n  public $f = 1;\n  function m() { return $this->f; }\n}\n"

	csharpSrc := "class A {\n" +
		"    int F = 1;\n" +
		"    int M(int x) { return x + 1; }\n" +
		"    int N(int y) { return y * 2; }\n" +
		"}\n\n" +
		"class B {\n" +
		"    string S = \"hi\";\n" +
		"}\n"

	cssSrc := ".a {\n  color: red;\n  margin: 0;\n}\n\n" +
		".b {\n  padding: 1px;\n}\n\n" +
		"#c {\n  display: block;\n  width: 10px;\n}\n"

	goLang := grammars.GoLanguage()
	return []oeditSweepCase{
		{"go", goLang, []byte(goSrc)},
		{"go-switch", goLang, []byte(goSwitch)},
		{"go-comments", goLang, []byte(goComments)},
		{"go-decls", goLang, []byte(goDecls)},
		{"go-select", goLang, []byte(goSelect)},
		{"scala", grammars.ScalaLanguage(), []byte(scalaSrc)},
		{"php", grammars.PhpLanguage(), []byte(phpSrc)},
		{"c_sharp", grammars.CSharpLanguage(), []byte(csharpSrc)},
		{"css", grammars.CssLanguage(), []byte(cssSrc)},
	}
}

func TestOEditRangeNormalizeByteSweep(t *testing.T) {
	for _, tc := range oeditSweepCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			runOEditSweep(t, tc)
		})
	}
}

func runOEditSweep(t *testing.T, tc oeditSweepCase) {
	t.Helper()
	freshP := gts.NewParser(tc.lang)
	baseP := gts.NewParser(tc.lang)
	incrP := gts.NewParser(tc.lang)

	baseline, err := baseP.Parse(tc.src)
	if err != nil || baseline == nil || baseline.RootNode() == nil {
		t.Fatalf("baseline parse: %v", err)
	}
	defer baseline.Release()
	if e, m := oeditTreeStats(baseline.RootNode()); e != 0 || m != 0 {
		t.Fatalf("baseline source must be clean (err=%d miss=%d)", e, m)
	}

	clean, checked := 0, 0
	for i := 1; i < len(tc.src)-1; i++ {
		for _, cls := range []string{"insert", "delete", "replace"} {
			edited, edit := incrGateBuildEdit(tc.src, i, cls)

			fresh, err := freshP.Parse(edited)
			if err != nil || fresh == nil || fresh.RootNode() == nil {
				if fresh != nil {
					fresh.Release()
				}
				continue
			}
			if int(fresh.RootNode().EndByte()) != len(edited) {
				fresh.Release()
				continue
			}
			if e, m := oeditTreeStats(fresh.RootNode()); e != 0 || m != 0 {
				fresh.Release()
				continue
			}
			clean++

			// Range-limited incremental parse (the change under test).
			incrP.SetForceFullResultNormalizationWalk(false)
			oldCopy := baseline.Copy()
			oldCopy.Edit(edit)
			incr, err := incrP.ParseIncremental(edited, oldCopy)
			if err != nil || incr == nil || incr.RootNode() == nil {
				t.Fatalf("%s pos=%d class=%s: incremental parse failed while fresh was clean", tc.name, i, cls)
			}
			incrS := oeditSerialize(incr.RootNode(), tc.lang)

			// Full-walk incremental parse (main behavior, range-limiting off).
			incrP.SetForceFullResultNormalizationWalk(true)
			oldCopyFull := baseline.Copy()
			oldCopyFull.Edit(edit)
			full, err := incrP.ParseIncremental(edited, oldCopyFull)
			if err != nil || full == nil || full.RootNode() == nil {
				t.Fatalf("%s pos=%d class=%s: full-walk incremental parse failed", tc.name, i, cls)
			}
			fullS := oeditSerialize(full.RootNode(), tc.lang)
			incrP.SetForceFullResultNormalizationWalk(false)
			checked++

			// Primary isolation invariant: range-limiting must be a no-op vs the
			// full walk. Any difference here is a regression THIS change caused.
			if incrS != fullS {
				t.Fatalf("%s pos=%d class=%s: range-limited != full-walk (regression)\n  full=%s\n  incr=%s",
					tc.name, i, cls, oeditTrunc(fullS), oeditTrunc(incrS))
			}

			// Secondary: where the full walk already matches fresh, the
			// range-limited walk must too (pre-existing full-walk vs fresh
			// divergences are out of scope -- tracked by the invariant gate).
			freshS := oeditSerialize(fresh.RootNode(), tc.lang)
			if fullS == freshS && incrS != freshS {
				t.Fatalf("%s pos=%d class=%s: incremental != fresh while full-walk == fresh\n  fresh=%s\n  incr =%s",
					tc.name, i, cls, oeditTrunc(freshS), oeditTrunc(incrS))
			}
			oldCopy.Release()
			oldCopyFull.Release()
			fresh.Release()
			incr.Release()
			full.Release()
		}
	}
	t.Logf("%s: freshCleanSites=%d checked=%d", tc.name, clean, checked)
	if checked == 0 {
		t.Fatalf("%s: no freshly-clean sites checked -- sweep did not exercise the invariant", tc.name)
	}
}

// oeditSerialize records every node's type, byte span, named/anonymous status,
// and missing flag, and walks ALL children (anonymous included). Unlike SExpr it
// captures exactly the state the range-limited normalizers mutate: endByte
// span adjustments and dropped anonymous `;` children.
func oeditSerialize(n *gts.Node, lang *gts.Language) string {
	var b strings.Builder
	var walk func(*gts.Node)
	walk = func(x *gts.Node) {
		if x == nil {
			b.WriteString("<nil>")
			return
		}
		b.WriteByte('(')
		b.WriteString(x.Type(lang))
		fmt.Fprintf(&b, ":%d-%d", x.StartByte(), x.EndByte())
		if x.IsNamed() {
			b.WriteString(":n")
		} else {
			b.WriteString(":a")
		}
		if x.IsMissing() {
			b.WriteString(":miss")
		}
		if x.IsError() {
			b.WriteString(":err")
		}
		cc := x.ChildCount()
		for i := 0; i < cc; i++ {
			b.WriteByte(' ')
			walk(x.Child(i))
		}
		b.WriteByte(')')
	}
	walk(n)
	return b.String()
}

func oeditTreeStats(n *gts.Node) (errN, missN int) {
	return incrGateTreeStats(n)
}

func oeditTrunc(s string) string {
	const max = 400
	if len(s) <= max {
		return s
	}
	return fmt.Sprintf("%s...(%d more)", s[:max], len(s)-max)
}
