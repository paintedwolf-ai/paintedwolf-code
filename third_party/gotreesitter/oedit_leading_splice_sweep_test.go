package gotreesitter_test

// Adversarial byte-sweep differential for the campaign post-admission-frontier
// T2a leading-run block-splice (spec.campaign.post-admission-frontier). For
// every insert / delete / replace edit at every byte position of an adversarial
// multi-item source, the incremental parse must be IDENTICAL to a fresh parse of
// the same edited text, at every site where the fresh parse is genuinely clean
// (full-span, zero IsError/IsMissing).
//
// Identity is checked with oeditSerialize (the hardened #418 serializer: every
// node's type, [startByte-endByte], named-vs-anonymous, missing, error, walking
// ALL children including anonymous), NOT SExpr -- the leading splice reuses whole
// top-level items across the edit, exactly the span/anonymous-child state SExpr
// hides.
//
// Fixtures target the leading-splice boundary cases the design must survive:
// comments between top-level items, extras before the first item, edits into
// item 0, an untyped-parameter-list function (Go's tracked ambiguity construct)
// sitting in the spliced leading run, and (for css) the production route the
// splice runs on.
//
// The JavaScript family is always compared directly with a fresh parse of the
// edited source. Comparing it only to the old non-splicing incremental path is
// not a sound admission gate: that old path can itself differ from fresh, and
// the splice can repair the divergence. Legacy fixtures with separately tracked
// incremental residuals keep the narrower splice-on/splice-off differential so
// this test does not absorb unrelated correctness work.
//
// Coverage spans the leading-enabled languages (go, production-route css), the
// reuse-declining one (python), and the ambiguous-expression / ASI family
// (typescript, tsx, javascript).

import (
	"fmt"
	"strings"
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

type leadingSweepCase struct {
	name         string
	lang         *gts.Language
	src          []byte
	requireFresh bool
}

func leadingSweepCases() []leadingSweepCase {
	goLang := grammars.GoLanguage()

	// Comments between items + a leading file comment + a block comment.
	goComments := []byte("package p\n\n" +
		"// leading file comment\n" +
		"func A0(a int) int {\n\treturn a\n}\n\n" +
		"// between A0 and A1\n" +
		"func A1(b int) int {\n\treturn b * 2\n}\n\n" +
		"/* block between A1 and A2 */\n" +
		"func A2(c int) int {\n\tif c > 0 {\n\t\treturn c\n\t}\n\treturn 0\n}\n\n" +
		"func A3(d int) int {\n\treturn d\n}\n")

	// An untyped-parameter-list function (tracked ambiguity) as a LEADING item,
	// plus later clean functions to edit so the untyped-param function splices.
	goUntyped := []byte("package p\n\n" +
		"func h(a, b int) int {\n\treturn a\n}\n\n" +
		"func C0(x int) int {\n\treturn x\n}\n\n" +
		"func C1(x int) int {\n\treturn x + 1\n}\n\n" +
		"func C2(x int) int {\n\treturn x + 2\n}\n")

	// Mixed top-level declaration forms with an import/const/var run ahead of a
	// function, so the leading run crosses several distinct top-level symbols.
	goDecls := []byte("package p\n\n" +
		"import \"fmt\"\n\n" +
		"const A = 1\n\n" +
		"var x = 2\n\n" +
		"type T struct {\n\tF int\n}\n\n" +
		"func F() { fmt.Println(x) }\n\n" +
		"func G() int { return A }\n")

	// CSS on the production route: comments between rules + a leading comment.
	cssComments := []byte("/* header comment */\n" +
		".a {\n  color: red;\n  margin: 0;\n}\n\n" +
		"/* between a and b */\n" +
		".b {\n  padding: 1px;\n}\n\n" +
		"#c {\n  display: block;\n  width: 10px;\n}\n\n" +
		".d {\n  color: blue;\n}\n")

	// Python: comments between defs + a leading module comment. Python declines
	// reuse today, so incremental == fresh is the trivial invariant here, and the
	// sweep confirms the shared leading-splice plumbing stays neutral for it.
	pyComments := []byte("# module comment\n" +
		"def f0(a):\n    return a\n\n" +
		"# between f0 and f1\n" +
		"def f1(a):\n    v = a + 1\n    return v\n\n" +
		"def f2(a):\n    return a * 2\n")

	// TypeScript / TSX: comments between items exercise the ambiguous-expression
	// / ASI family admitted by #429.
	tsComments := []byte("// module header\n" +
		"export function f0(a: number): number {\n  return a;\n}\n\n" +
		"// between f0 and f1\n" +
		"export function f1(a: number): number {\n  const v = a + 1;\n  return v;\n}\n\n" +
		"interface Shape {\n  width: number;\n  height: number;\n}\n")

	jsComments := []byte("// module header\n" +
		"function f0(a) {\n  return a;\n}\n\n" +
		"// between f0 and f1\n" +
		"function f1(a) {\n  const v = a + 1;\n  return v;\n}\n\n" +
		"const g = (x) => x * 2;\n")

	return []leadingSweepCase{
		{"go-comments", goLang, goComments, false},
		{"go-untyped-leading", goLang, goUntyped, false},
		{"go-decls", goLang, goDecls, false},
		{"css-comments", grammars.CssLanguage(), cssComments, false},
		{"python-comments", grammars.PythonLanguage(), pyComments, false},
		{"typescript-comments", grammars.TypescriptLanguage(), tsComments, true},
		{"tsx-comments", grammars.TsxLanguage(), tsComments, true},
		{"javascript-comments", grammars.JavascriptLanguage(), jsComments, true},
	}
}

func TestLeadingSpliceByteSweep(t *testing.T) {
	for _, tc := range leadingSweepCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			runLeadingSweep(t, tc)
		})
	}
}

func runLeadingSweep(t *testing.T, tc leadingSweepCase) {
	t.Helper()
	baseP := gts.NewParser(tc.lang)
	baseline, err := baseP.Parse(tc.src)
	if err != nil || baseline == nil || baseline.RootNode() == nil {
		t.Fatalf("baseline parse: %v", err)
	}
	defer baseline.Release()
	if e, m := incrGateTreeStats(baseline.RootNode()); e != 0 || m != 0 {
		t.Fatalf("baseline source must be clean (err=%d miss=%d)", e, m)
	}

	freshP := gts.NewParser(tc.lang)
	incrP := gts.NewParser(tc.lang)
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
			if e, m := incrGateTreeStats(fresh.RootNode()); e != 0 || m != 0 {
				fresh.Release()
				continue
			}
			clean++

			old := baseline.Copy()
			old.Edit(edit)
			incr, err := incrP.ParseIncremental(edited, old)
			if err != nil || incr == nil || incr.RootNode() == nil {
				t.Fatalf("%s pos=%d class=%s: incremental parse failed while fresh was clean", tc.name, i, cls)
			}
			incrS := oeditSerialize(incr.RootNode(), tc.lang)
			checked++

			freshS := oeditSerialize(fresh.RootNode(), tc.lang)
			if tc.requireFresh {
				if incrS != freshS {
					t.Fatalf("%s pos=%d class=%s: incremental != fresh\n  fresh=%s\n  incr =%s",
						tc.name, i, cls, oeditTrunc(freshS), oeditTrunc(incrS))
				}
			} else {
				incrP.SetDisableLeadingRunSplice(true)
				oldOff := baseline.Copy()
				oldOff.Edit(edit)
				off, offErr := incrP.ParseIncremental(edited, oldOff)
				incrP.SetDisableLeadingRunSplice(false)
				if offErr != nil || off == nil || off.RootNode() == nil {
					t.Fatalf("%s pos=%d class=%s: leading-splice-off parse failed: %v", tc.name, i, cls, offErr)
				}
				offS := oeditSerialize(off.RootNode(), tc.lang)
				if incrS != offS {
					t.Fatalf("%s pos=%d class=%s: leading splice changed a legacy fixture\n  off =%s\n  incr=%s",
						tc.name, i, cls, oeditTrunc(offS), oeditTrunc(incrS))
				}
				oldOff.Release()
				off.Release()
			}
			old.Release()
			fresh.Release()
			incr.Release()
		}
	}
	t.Logf("%s: freshCleanSites=%d checked=%d", tc.name, clean, checked)
	if checked == 0 {
		t.Fatalf("%s: no freshly-clean sites checked -- sweep did not exercise the invariant", tc.name)
	}
}

// TestLeadingSpliceJavaScriptFamilyIsBoundedNotFileSized proves admission is
// active for JavaScript, TypeScript, and TSX and that leading work stays flat
// when a middle edit's unchanged prefix grows by an order of magnitude.
func TestLeadingSpliceJavaScriptFamilyIsBoundedNotFileSized(t *testing.T) {
	build := func(fmtStr string, n int) []byte {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, fmtStr, i, i)
		}
		return []byte(b.String())
	}
	cases := []struct {
		name   string
		lang   *gts.Language
		format string
	}{
		{"typescript", grammars.TypescriptLanguage(), "export function f%d(a: number): number {\n  const v = a + %d;\n  return v;\n}\n\n"},
		{"tsx", grammars.TsxLanguage(), "export function f%d(a: number): number {\n  const v = a + %d;\n  return v;\n}\n\n"},
		{"javascript", grammars.JavascriptLanguage(), "function f%d(a) {\n  const v = a + %d;\n  return v;\n}\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := func(n int) gts.IncrementalParseProfile {
				src := build(tc.format, n)
				mark := fmt.Sprintf("f%d(", n/2)
				mi := strings.Index(string(src), mark)
				if mi < 0 {
					t.Fatalf("marker %q not found", mark)
				}
				off := mi + len(mark) - 2 // a digit inside the function index
				fresh, incr, prof, edited := leadingParseAt(t, tc.lang, src, off, "insert")
				if int(incr.EndByte()) != len(edited) {
					t.Fatalf("incremental root span %d does not cover edited buffer %d", incr.EndByte(), len(edited))
				}
				freshS := oeditSerialize(fresh, tc.lang)
				incrS := oeditSerialize(incr, tc.lang)
				if incrS != freshS {
					t.Fatalf("incremental != fresh\n  fresh=%s\n  incr =%s", oeditTrunc(freshS), oeditTrunc(incrS))
				}
				if prof.BlockSpliceSteps == 0 {
					t.Fatal("leading splice did not fire")
				}
				return prof
			}
			small := run(200)
			large := run(2000)
			const bound = 64
			if small.ReuseRejectRootNonLeafChanged > bound || large.ReuseRejectRootNonLeafChanged > bound {
				t.Fatalf("leading reuse is O(prefix), not O(edit): rejects small=%d large=%d (bound %d)",
					small.ReuseRejectRootNonLeafChanged, large.ReuseRejectRootNonLeafChanged, bound)
			}
			if large.ReuseRejectRootNonLeafChanged > small.ReuseRejectRootNonLeafChanged+16 {
				t.Fatalf("leading reuse grew with the unchanged prefix: rejects small=%d large=%d",
					small.ReuseRejectRootNonLeafChanged, large.ReuseRejectRootNonLeafChanged)
			}
		})
	}
}
