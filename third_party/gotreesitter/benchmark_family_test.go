package gotreesitter_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

type familyBenchmarkSpec struct {
	family   string
	language string
	lang     func() *gotreesitter.Language
	source   func(int) []byte
}

var familyParseBenchmarkSpecs = []familyBenchmarkSpec{
	{family: "apple", language: "swift", lang: grammars.SwiftLanguage, source: makeSwiftBenchmarkSource},
	{family: "c-family", language: "c", lang: grammars.CLanguage, source: makeCBenchmarkSource},
	{family: "data", language: "json", lang: grammars.JsonLanguage, source: makeJSONBenchmarkSource},
	{family: "dotnet", language: "csharp", lang: grammars.CSharpLanguage, source: makeCSharpBenchmarkSource},
	{family: "jvm", language: "java", lang: grammars.JavaLanguage, source: makeJavaBenchmarkSource},
	{family: "jvm", language: "kotlin", lang: grammars.KotlinLanguage, source: makeKotlinBenchmarkSource},
	{family: "markup", language: "html", lang: grammars.HtmlLanguage, source: makeHTMLBenchmarkSource},
	{family: "scientific", language: "fortran", lang: grammars.FortranLanguage, source: makeFortranBenchmarkSource},
	{family: "scripting", language: "bash", lang: grammars.BashLanguage, source: makeBashBenchmarkSource},
	{family: "scripting", language: "python", lang: grammars.PythonLanguage, source: makePythonBenchmarkSource},
	{family: "scripting", language: "ruby", lang: grammars.RubyLanguage, source: makeRubyBenchmarkSource},
	{family: "styles", language: "css", lang: grammars.CssLanguage, source: makeCSSBenchmarkSource},
	{family: "systems", language: "go", lang: grammars.GoLanguage, source: makeGoBenchmarkSource},
	{family: "systems", language: "rust", lang: grammars.RustLanguage, source: makeRustBenchmarkSource},
	{family: "web", language: "javascript", lang: grammars.JavascriptLanguage, source: makeJavaScriptBenchmarkSource},
	{family: "web", language: "typescript", lang: grammars.TypescriptLanguage, source: makeTypeScriptBenchmarkSource},
	{family: "web", language: "tsx", lang: grammars.TsxLanguage, source: makeTSXBenchmarkSource},
}

func BenchmarkFamilyParseFullDFA(b *testing.B) {
	unitCount := familyBenchmarkUnitCount(b)
	for _, spec := range familyParseBenchmarkSpecs {
		spec := spec
		b.Run(spec.family+"/"+spec.language, func(b *testing.B) {
			benchmarkParseFullDFA(b, dfaBenchmarkSpec{
				name:            spec.language,
				lang:            spec.lang,
				source:          spec.source,
				funcCount:       unitCount,
				requireNoErrors: true,
				warmupBeforeRun: true,
			})
		})
	}
}

func familyBenchmarkUnitCount(b *testing.B) int {
	if strings.TrimSpace(os.Getenv("GOT_BENCH_FUNC_COUNT")) != "" {
		return benchmarkFuncCount(b)
	}
	if testing.Short() {
		return 10
	}
	return 25
}

func makeJavaScriptBenchmarkSource(funcCount int) []byte {
	var sb strings.Builder
	sb.Grow(funcCount * 80)
	for i := 0; i < funcCount; i++ {
		fmt.Fprintf(&sb, "export function f%d() { const v = %d; return v }\n", i, i)
	}
	return []byte(sb.String())
}

func makeTSXBenchmarkSource(componentCount int) []byte {
	var sb strings.Builder
	sb.Grow(componentCount * 150)
	sb.WriteString("import React from 'react'\n\n")
	for i := 0; i < componentCount; i++ {
		fmt.Fprintf(&sb, "export function C%d() { const v = %d; return <section data-id={v}><span>{v}</span></section> }\n", i, i)
	}
	return []byte(sb.String())
}

func makeJSONBenchmarkSource(itemCount int) []byte {
	var sb strings.Builder
	sb.Grow(itemCount * 96)
	sb.WriteString("{\"items\":[")
	for i := 0; i < itemCount; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "{\"id\":%d,\"name\":\"item-%d\",\"enabled\":%t,\"tags\":[\"alpha\",\"beta\"]}", i, i, i%2 == 0)
	}
	sb.WriteString("]}\n")
	return []byte(sb.String())
}

func makeCSSBenchmarkSource(ruleCount int) []byte {
	var sb strings.Builder
	sb.Grow(ruleCount * 140)
	for i := 0; i < ruleCount; i++ {
		fmt.Fprintf(&sb, ".item-%d { color: #%06x; margin: %dpx; padding: 4px; }\n", i, (uint32(i)*uint32(2654435761))&0xffffff, i%24)
		fmt.Fprintf(&sb, "@media (min-width: 640px) { .item-%d:hover { transform: translateX(%dpx); } }\n", i, i%16)
	}
	return []byte(sb.String())
}

func makeHTMLBenchmarkSource(sectionCount int) []byte {
	var sb strings.Builder
	sb.Grow(sectionCount * 140)
	sb.WriteString("<!doctype html><html><body>\n")
	for i := 0; i < sectionCount; i++ {
		fmt.Fprintf(&sb, "<section id=\"item-%d\"><h2>Item %d</h2><p data-id=\"%d\">Hello <span>world</span></p></section>\n", i, i, i)
	}
	sb.WriteString("</body></html>\n")
	return []byte(sb.String())
}

func makeBashBenchmarkSource(funcCount int) []byte {
	var sb strings.Builder
	sb.Grow(funcCount * 70)
	sb.WriteString("#!/usr/bin/env bash\nset -euo pipefail\n")
	for i := 0; i < funcCount; i++ {
		fmt.Fprintf(&sb, "f%d() { local v=%d; echo \"$v\"; }\n", i, i)
	}
	return []byte(sb.String())
}

func makeCBenchmarkSource(funcCount int) []byte {
	var sb strings.Builder
	sb.Grow(funcCount * 64)
	sb.WriteString("#include <stdio.h>\n")
	for i := 0; i < funcCount; i++ {
		fmt.Fprintf(&sb, "int f%d(void) { int v = %d; return v; }\n", i, i)
	}
	return []byte(sb.String())
}

func makeCSharpBenchmarkSource(classCount int) []byte {
	var sb strings.Builder
	sb.Grow(classCount * 115)
	sb.WriteString("namespace Bench {\n")
	for i := 0; i < classCount; i++ {
		fmt.Fprintf(&sb, "public static class C%d { public static int F%d() { var v = %d; return v; } }\n", i, i, i)
	}
	sb.WriteString("}\n")
	return []byte(sb.String())
}

func makeJavaBenchmarkSource(classCount int) []byte {
	var sb strings.Builder
	sb.Grow(classCount * 90)
	sb.WriteString("package bench;\n")
	for i := 0; i < classCount; i++ {
		fmt.Fprintf(&sb, "class C%d { int f%d() { int v = %d; return v; } }\n", i, i, i)
	}
	return []byte(sb.String())
}

func makeKotlinBenchmarkSource(funcCount int) []byte {
	var sb strings.Builder
	sb.Grow(funcCount * 55)
	sb.WriteString("fun main() {\n")
	for i := 0; i < funcCount; i++ {
		fmt.Fprintf(&sb, "    val x%d: Int? = %d\n    println(x%d)\n", i, i, i)
	}
	sb.WriteString("}\n")
	return []byte(sb.String())
}

func makeSwiftBenchmarkSource(funcCount int) []byte {
	var sb strings.Builder
	sb.Grow(funcCount * 58)
	for i := 0; i < funcCount; i++ {
		fmt.Fprintf(&sb, "func f%d() -> Int {\n    let v = %d\n    return v\n}\n\n", i, i)
	}
	return []byte(sb.String())
}

func makeFortranBenchmarkSource(funcCount int) []byte {
	var sb strings.Builder
	sb.Grow(funcCount * 90)
	sb.WriteString("program bench\ncontains\n")
	for i := 0; i < funcCount; i++ {
		fmt.Fprintf(&sb, "  integer function f%d()\n    integer :: v\n    v = %d\n    f%d = v\n  end function f%d\n", i, i, i, i)
	}
	sb.WriteString("end program bench\n")
	return []byte(sb.String())
}

func makeRubyBenchmarkSource(funcCount int) []byte {
	var sb strings.Builder
	sb.Grow(funcCount * 35)
	for i := 0; i < funcCount; i++ {
		fmt.Fprintf(&sb, "def f%d\n  v = %d\n  v\nend\n\n", i, i)
	}
	return []byte(sb.String())
}

func makeRustBenchmarkSource(funcCount int) []byte {
	var sb strings.Builder
	sb.Grow(funcCount * 55)
	for i := 0; i < funcCount; i++ {
		fmt.Fprintf(&sb, "pub fn f%d() -> i32 { let v = %d; v }\n", i, i)
	}
	return []byte(sb.String())
}
