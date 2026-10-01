package taproot_test

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter/grammargen"
	"github.com/odvcencio/gotreesitter/taproot"
)

// tinyGrammar builds a minimal grammar: a program is exactly
// "identifier ; identifier". Valid: "a ; b". Invalid: "a b" (missing ';').
// The mandatory semicolon ensures predictable MISSING/ERROR nodes.
func tinyGrammar() *grammargen.Grammar {
	g := grammargen.NewGrammar("tiny")
	g.Define("program", grammargen.Seq(
		grammargen.Field("first", grammargen.Sym("identifier")),
		grammargen.Str(";"),
		grammargen.Field("second", grammargen.Sym("identifier")),
	))
	g.Define("identifier", grammargen.Token(grammargen.Repeat1(grammargen.Pat(`[a-zA-Z_]`))))
	g.SetExtras(grammargen.Pat(`[ \t\r\n]+`))
	g.SetWord("identifier")
	return g
}

// TestLanguageCachesOnce checks that Language() returns the same *Language
// pointer on repeated calls and invokes build exactly once.
func TestLanguageCachesOnce(t *testing.T) {
	calls := 0
	build := func() *grammargen.Grammar {
		calls++
		return tinyGrammar()
	}

	l1, err := taproot.Language("cachetest", build)
	if err != nil {
		t.Fatalf("first Language call failed: %v", err)
	}
	if l1 == nil {
		t.Fatal("Language returned nil")
	}

	l2, err := taproot.Language("cachetest", build)
	if err != nil {
		t.Fatalf("second Language call failed: %v", err)
	}
	if l1 != l2 {
		t.Error("Language did not return cached pointer on second call")
	}
	if calls != 1 {
		t.Errorf("build invoked %d times; want 1", calls)
	}
}

func TestLanguageFromBlobUsesBlobWithoutBuild(t *testing.T) {
	_, blob, err := grammargen.GenerateLanguageAndBlob(tinyGrammar())
	if err != nil {
		t.Fatalf("GenerateLanguageAndBlob: %v", err)
	}

	calls := 0
	lang, err := taproot.LanguageFromBlob("blobtest", blob, func() *grammargen.Grammar {
		calls++
		return tinyGrammar()
	})
	if err != nil {
		t.Fatalf("LanguageFromBlob: %v", err)
	}
	if lang == nil {
		t.Fatal("LanguageFromBlob returned nil")
	}
	if calls != 0 {
		t.Errorf("build invoked %d times; want 0", calls)
	}

	root, w, err := taproot.ParseFromBlob("blobparse", blob, nil, []byte("hello ; world"))
	if err != nil {
		t.Fatalf("ParseFromBlob: %v", err)
	}
	if root == nil || w == nil {
		t.Fatalf("ParseFromBlob returned root=%v walker=%v", root, w)
	}
}

func TestLanguageFromBlobFallsBackToBuild(t *testing.T) {
	calls := 0
	lang, err := taproot.LanguageFromBlob("blobfallback", []byte("not a language blob"), func() *grammargen.Grammar {
		calls++
		return tinyGrammar()
	})
	if err != nil {
		t.Fatalf("LanguageFromBlob fallback: %v", err)
	}
	if lang == nil {
		t.Fatal("LanguageFromBlob fallback returned nil")
	}
	if calls != 1 {
		t.Errorf("build invoked %d times; want 1", calls)
	}
}

// TestParseValid checks that valid source parses without error and returns a
// walkable root.
func TestParseValid(t *testing.T) {
	root, w, err := taproot.Parse("tinyvalid", tinyGrammar, []byte("hello ; world"))
	if err != nil {
		t.Fatalf("Parse returned unexpected error: %v", err)
	}
	if root == nil {
		t.Fatal("Parse returned nil root")
	}
	if w == nil {
		t.Fatal("Parse returned nil Walker")
	}
	// Check that Walker.Type works on the root node.
	typ := w.Type(root)
	if typ == "" {
		t.Error("Walker.Type returned empty string for root")
	}
}

// TestParseInvalid checks that broken source returns a non-nil error whose
// message includes a line:col position and "expected" or "near".
func TestParseInvalid(t *testing.T) {
	// Missing semicolon between the two identifiers triggers an ERROR/MISSING node.
	root, w, err := taproot.Parse("tinyinvalid", tinyGrammar, []byte("hello world"))
	if err == nil {
		t.Fatal("expected error from broken source, got nil")
	}
	msg := err.Error()
	// Must contain a line:col reference.
	if !strings.Contains(msg, ":") {
		t.Errorf("error message %q does not contain ':' (expected line:col)", msg)
	}
	// Must contain "expected" (missing token) or "near" (error token).
	if !strings.Contains(msg, "expected") && !strings.Contains(msg, "near") {
		t.Errorf("error message %q contains neither 'expected' nor 'near'", msg)
	}
	// root and walker should still be returned even on error.
	if root == nil {
		t.Error("Parse should return root node even on syntax error")
	}
	if w == nil {
		t.Error("Parse should return walker even on syntax error")
	}
}

// TestWalkerPos checks that Pos returns 1-based line and column.
func TestWalkerPos(t *testing.T) {
	src := []byte("hello ; world")
	root, w, err := taproot.Parse("tinypos", tinyGrammar, src)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	// Use the "first" field (the first identifier) to test Pos.
	child := w.Field(root, "first")
	if child == nil {
		t.Skip("no 'first' field to test Pos on")
	}
	line, col := w.Pos(child)
	if line < 1 {
		t.Errorf("Pos line = %d; want >= 1", line)
	}
	if col < 1 {
		t.Errorf("Pos col = %d; want >= 1", col)
	}
}

// TestWalkerText checks that Walker.Text and Walker.Field return correct values.
func TestWalkerText(t *testing.T) {
	src := []byte("hello ; world")
	root, w, err := taproot.Parse("tinytxt", tinyGrammar, src)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	// The program's "first" field is the first identifier.
	firstNode := w.Field(root, "first")
	if firstNode == nil {
		t.Fatal("Field(root, 'first') returned nil")
	}
	got := w.Text(firstNode)
	if got != "hello" {
		t.Errorf("Text(first) = %q; want %q", got, "hello")
	}
	// The program's "second" field is the second identifier.
	secondNode := w.Field(root, "second")
	if secondNode == nil {
		t.Fatal("Field(root, 'second') returned nil")
	}
	got2 := w.Text(secondNode)
	if got2 != "world" {
		t.Errorf("Text(second) = %q; want %q", got2, "world")
	}
}

func TestWalkerChildByType(t *testing.T) {
	src := []byte("hello ; world")
	root, w, err := taproot.Parse("tinychildbytype", tinyGrammar, src)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	child := w.ChildByType(root, "identifier")
	if child == nil {
		t.Fatal("ChildByType(root, identifier) returned nil")
	}
	if got := w.Text(child); got != "hello" {
		t.Errorf("ChildByType text = %q; want hello", got)
	}
	if missing := w.ChildByType(root, "does_not_exist"); missing != nil {
		t.Errorf("ChildByType(root, does_not_exist) = %v; want nil", missing)
	}
}
