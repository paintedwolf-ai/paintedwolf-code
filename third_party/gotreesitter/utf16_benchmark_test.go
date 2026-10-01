package gotreesitter

import (
	"strings"
	"testing"
	"unicode/utf16"
)

var (
	benchmarkUTF16BytesSink []byte
	benchmarkUTF16MapSink   *utf16SourceMap
	benchmarkUTF16EndSink   uint32
)

func makeUTF16ArithmeticBenchmarkSource(termCount int) []uint16 {
	var b strings.Builder
	b.Grow(termCount * 2)
	for i := 0; i < termCount; i++ {
		if i > 0 {
			b.WriteByte('+')
		}
		b.WriteByte('1')
	}
	b.WriteByte('\n')
	return utf16.Encode([]rune(b.String()))
}

func BenchmarkUTF16SourceMapConversion(b *testing.B) {
	source := makeUTF16ArithmeticBenchmarkSource(256)
	b.ReportAllocs()
	b.SetBytes(int64(len(source) * 2))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		utf8Source, sourceMap := encodeUTF16ToUTF8WithMap(source)
		benchmarkUTF16BytesSink = utf8Source
		benchmarkUTF16MapSink = sourceMap
	}
}

func BenchmarkParseUTF16Arithmetic(b *testing.B) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	source := makeUTF16ArithmeticBenchmarkSource(256)

	b.ReportAllocs()
	b.SetBytes(int64(len(source) * 2))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		tree, err := parser.ParseUTF16(source)
		if err != nil {
			b.Fatalf("ParseUTF16 failed: %v", err)
		}
		if tree == nil || tree.RootNode() == nil || tree.RootNode().HasError() {
			b.Fatalf("ParseUTF16 returned invalid tree")
		}
		benchmarkUTF16EndSink = tree.RootNode().EndByte()
		tree.Release()
	}
}

func BenchmarkParseUTF8ArithmeticBaseline(b *testing.B) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	source, _ := encodeUTF16ToUTF8WithMap(makeUTF16ArithmeticBenchmarkSource(256))

	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		tree, err := parser.Parse(source)
		if err != nil {
			b.Fatalf("Parse failed: %v", err)
		}
		if tree == nil || tree.RootNode() == nil || tree.RootNode().HasError() {
			b.Fatalf("Parse returned invalid tree")
		}
		benchmarkUTF16EndSink = tree.RootNode().EndByte()
		tree.Release()
	}
}
