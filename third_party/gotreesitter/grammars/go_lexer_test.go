//go:build !grammar_subset || grammar_subset_go

package grammars

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestNewGoTokenSourceReturnsErrorOnMissingSymbols(t *testing.T) {
	lang := &gotreesitter.Language{
		TokenCount:  1,
		SymbolNames: []string{"end"},
	}

	if _, err := NewGoTokenSource([]byte("package main\n"), lang); err == nil {
		t.Fatal("expected error for language missing go token symbols")
	}
}

func TestNewGoTokenSourceOrEOFFallsBack(t *testing.T) {
	lang := &gotreesitter.Language{
		TokenCount:  1,
		SymbolNames: []string{"end"},
	}

	ts := NewGoTokenSourceOrEOF([]byte("package main\n"), lang)
	tok := ts.Next()
	if tok.Symbol != 0 {
		t.Fatalf("fallback token symbol = %d, want EOF (0)", tok.Symbol)
	}
}

func TestGoTokenSourceSkipToByteReseek(t *testing.T) {
	lang := GoLanguage()

	var b strings.Builder
	b.WriteString("package main\n\nfunc main() {\n")
	for i := 0; i < 900; i++ {
		b.WriteString("\tx := 1\n")
	}
	b.WriteString("\ttarget := 2\n")
	b.WriteString("}\n")
	src := []byte(b.String())

	targetOffset := strings.Index(b.String(), "target")
	if targetOffset < 0 {
		t.Fatal("missing target marker")
	}

	ts, err := NewGoTokenSource(src, lang)
	if err != nil {
		t.Fatalf("NewGoTokenSource failed: %v", err)
	}

	tok := ts.SkipToByte(uint32(targetOffset))
	if tok.Symbol == 0 {
		t.Fatal("SkipToByte unexpectedly returned EOF")
	}
	if int(tok.StartByte) < targetOffset {
		t.Fatalf("token starts before target offset: got %d, target %d", tok.StartByte, targetOffset)
	}
	if tok.Text != "target" {
		t.Fatalf("expected identifier token text %q, got %q", "target", tok.Text)
	}
}

func TestGoTokenSourceRuneLiteralColumnsCountUTF8Bytes(t *testing.T) {
	lang := GoLanguage()
	src := []byte("package p\nvar _ = []struct{ from, to rune }{{'Å', 'Å'}}\n")

	offset := strings.Index(string(src), "'Å'")
	if offset < 0 {
		t.Fatal("missing rune literal")
	}

	ts, err := NewGoTokenSource(src, lang)
	if err != nil {
		t.Fatalf("NewGoTokenSource failed: %v", err)
	}

	tok := ts.SkipToByte(uint32(offset))
	if tok.Text != "'Å'" {
		t.Fatalf("SkipToByte token = %q, want %q", tok.Text, "'Å'")
	}

	gotWidth := tok.EndPoint.Column - tok.StartPoint.Column
	wantWidth := uint32(len(tok.Text))
	if gotWidth != wantWidth {
		t.Fatalf("rune literal column width = %d, want %d", gotWidth, wantWidth)
	}
}

func TestGoTokenSourceSplitsInterpretedStringEscapes(t *testing.T) {
	lang := GoLanguage()
	src := []byte("package p\nvar _ = \"\\u13b0\\uab80\"\n")

	ts, err := NewGoTokenSource(src, lang)
	if err != nil {
		t.Fatalf("NewGoTokenSource failed: %v", err)
	}

	var saw []string
	for {
		tok := ts.Next()
		if tok.Symbol == 0 {
			break
		}
		if tok.StartByte < uint32(strings.Index(string(src), "\"\\u13b0\\uab80\"")) || tok.EndByte > uint32(len(src)) {
			continue
		}
		switch tok.Text {
		case "\"", "\\u13b0", "\\uab80":
			saw = append(saw, tok.Text)
		}
	}

	got := strings.Join(saw, ",")
	want := "\",\\u13b0,\\uab80,\""
	if got != want {
		t.Fatalf("interpreted string token split = %q, want %q", got, want)
	}
}

func TestGoTokenSourceHandlesUnterminatedInterpretedString(t *testing.T) {
	lang := GoLanguage()
	src := []byte("package p\nvar _ = \"abc\n")

	ts, err := NewGoTokenSource(src, lang)
	if err != nil {
		t.Fatalf("NewGoTokenSource failed: %v", err)
	}

	stringOffset := strings.Index(string(src), "\"")
	if stringOffset < 0 {
		t.Fatal("missing opening quote")
	}

	var saw []string
	for {
		tok := ts.Next()
		if tok.Symbol == 0 {
			break
		}
		if int(tok.StartByte) < stringOffset {
			continue
		}
		switch tok.Text {
		case "\"", "abc":
			saw = append(saw, tok.Text)
		}
	}

	got := strings.Join(saw, ",")
	want := "\",abc"
	if got != want {
		t.Fatalf("unterminated interpreted string token split = %q, want %q", got, want)
	}
}

func TestGoTokenSourceHandlesUnterminatedRawString(t *testing.T) {
	lang := GoLanguage()
	src := []byte("package p\nvar _ = `abc\n")

	ts, err := NewGoTokenSource(src, lang)
	if err != nil {
		t.Fatalf("NewGoTokenSource failed: %v", err)
	}

	stringOffset := strings.Index(string(src), "`")
	if stringOffset < 0 {
		t.Fatal("missing opening backtick")
	}

	var saw []string
	for {
		tok := ts.Next()
		if tok.Symbol == 0 {
			break
		}
		if int(tok.StartByte) < stringOffset {
			continue
		}
		switch tok.Text {
		case "`", "abc\n":
			saw = append(saw, tok.Text)
		}
	}

	if len(saw) != 2 {
		t.Fatalf("unterminated raw string token count = %d, want 2 (%v)", len(saw), saw)
	}
	if saw[0] != "`" || saw[1] != "abc\n" {
		t.Fatalf("unterminated raw string token split = %q, want [\"`\", \"abc\\n\"]", saw)
	}
}

func TestGoTokenSourceParsesCasgstatusStyleIfPrintBlock(t *testing.T) {
	// GoTokenSource was calibrated to the ts2go-compiled Go blob's symbol
	// layout. As of 0.14.0 the default blob is grammargen-compiled with a
	// different layout (auto-semi split into distinct terminals,
	// `blank_identifier` as a non-terminal, etc.), and the custom lexer is
	// no longer the registered default. Driving it against the grammargen
	// blob produces a degraded parse by design. Skip when that's the blob
	// we're running against; run when callers supply a ts2go Go blob.
	lang := GoLanguage()
	if _, ok := lang.SymbolByName("source_file_token1"); !ok {
		t.Skip("GoTokenSource is ts2go-specific; current Go blob is grammargen-compiled (no source_file_token1)")
	}
	src := []byte(`package runtime

func casgstatus(gp *g, oldval, newval uint32) {
	if (oldval&_Gscan != 0) || (newval&_Gscan != 0) || oldval == newval {
		systemstack(func() {
			print("runtime: casgstatus: oldval=", hex(oldval), " newval=", hex(newval), "\n")
			throw("casgstatus: bad incoming values")
		})
	}

	if oldval == _Grunning && gp.gcscanvalid {
		print("runtime: casgstatus ", hex(oldval), "->", hex(newval), " gp.status=", hex(gp.atomicstatus), " gp.gcscanvalid=true\n")
		throw("casgstatus")
	}
}
`)

	ts, err := NewGoTokenSource(src, lang)
	if err != nil {
		t.Fatalf("NewGoTokenSource failed: %v", err)
	}

	parser := gotreesitter.NewParser(lang)
	tree, err := parser.ParseWithTokenSource(src, ts)
	if err != nil {
		t.Fatalf("ParseWithTokenSource failed: %v", err)
	}
	if tree == nil || tree.RootNode() == nil {
		t.Fatal("ParseWithTokenSource returned nil tree")
	}
	defer tree.Release()
	if got := tree.RootNode().Type(lang); got != "source_file" {
		t.Fatalf("root type = %q, want source_file: %s", got, tree.RootNode().SExpr(lang))
	}
	if tree.RootNode().HasError() {
		t.Fatalf("unexpected parse error: %s", tree.RootNode().SExpr(lang))
	}
}
