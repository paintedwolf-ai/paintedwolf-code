package gotreesitter_test

// Measurement harness for campaign O(edit) range-limited result normalization
// (spec.campaign.oedit). Reports the clean-Go near-top single-byte insert
// incremental reparse latency with the range-limited walk ON vs the full walk
// (SetForceFullResultNormalizationWalk), best-of-N warm iterations, at 137KB and
// 1MB, plus a CSS neutrality control. Run with:
//
//	go test -run TestOEditRangeNormalizeMeasure -v -timeout 20m

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func oeditMeasureGo(funcs int) []byte {
	var b strings.Builder
	b.WriteString("package p\n\n")
	for i := 0; i < funcs; i++ {
		fmt.Fprintf(&b, "func F%d(a int) int {\n\tx := a + %d\n\treturn x\n}\n\n", i, i)
	}
	return []byte(b.String())
}

func oeditMeasureCSS(rules int) []byte {
	var b strings.Builder
	for i := 0; i < rules; i++ {
		fmt.Fprintf(&b, ".r%d {\n  color: red;\n  margin: %dpx;\n  padding: 1px;\n}\n\n", i, i)
	}
	return []byte(b.String())
}

func oeditBestOf(t *testing.T, lang *gts.Language, src []byte, off int, iters int, fullWalk bool) time.Duration {
	t.Helper()
	edited := make([]byte, 0, len(src)+1)
	edited = append(edited, src[:off]...)
	edited = append(edited, 'z')
	edited = append(edited, src[off:]...)
	edit := gts.InputEdit{StartByte: uint32(off), OldEndByte: uint32(off), NewEndByte: uint32(off + 1)}

	parser := gts.NewParser(lang)
	parser.SetForceFullResultNormalizationWalk(fullWalk)

	best := time.Duration(1<<62 - 1)
	for it := 0; it < iters; it++ {
		base, err := parser.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		base.Edit(edit)
		start := time.Now()
		incr, err := parser.ParseIncremental(edited, base)
		el := time.Since(start)
		if err != nil {
			base.Release()
			t.Fatal(err)
		}
		if incr.RootNode().HasError() {
			base.Release()
			incr.Release()
			t.Fatal("incremental tree has error")
		}
		if el < best {
			best = el
		}
		base.Release()
		incr.Release()
	}
	return best
}

func TestOEditRangeNormalizeMeasure(t *testing.T) {
	if os.Getenv("OEDIT_MEASURE") == "" {
		t.Skip("measurement harness; set OEDIT_MEASURE=1 to run")
	}
	goLang := grammars.GoLanguage()
	cssLang := grammars.CssLanguage()

	type fixture struct {
		name  string
		lang  *gts.Language
		src   []byte
		mark  string
		iters int
	}
	fixtures := []fixture{
		{"go-137KB", goLang, oeditMeasureGo(2805), "func F0(a", 25},
		{"go-1MB", goLang, oeditMeasureGo(20971), "func F0(a", 15},
		{"css-neutral", cssLang, oeditMeasureCSS(6000), ".r0 {", 25},
	}
	for _, f := range fixtures {
		off := bytes.Index(f.src, []byte(f.mark)) + len(f.mark) - 1
		if off < 0 {
			t.Fatalf("%s: mark %q not found", f.name, f.mark)
		}
		full := oeditBestOf(t, f.lang, f.src, off, f.iters, true)
		limited := oeditBestOf(t, f.lang, f.src, off, f.iters, false)
		delta := full - limited
		t.Logf("%-14s bytes=%-8d fullWalk=%-8v rangeLimited=%-8v reduction=%-8v",
			f.name, len(f.src), full.Round(time.Microsecond), limited.Round(time.Microsecond), delta.Round(time.Microsecond))
	}
}
