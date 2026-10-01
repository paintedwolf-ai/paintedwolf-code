package gotreesitter

import (
	"bytes"
	"testing"
)

func TestRewriteSingleReplace(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	source := []byte("1+2")
	tree := mustParse(t, parser, source)

	root := tree.RootNode()
	// The last child should be NUMBER "2".
	num := root.Child(root.ChildCount() - 1)
	if num.Text(source) != "2" {
		t.Fatalf("expected last child text %q, got %q", "2", num.Text(source))
	}

	rw := NewRewriter(source)
	rw.Replace(num, []byte("42"))
	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "1+42" {
		t.Errorf("newSource = %q, want %q", newSource, "1+42")
	}

	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	e := edits[0]
	if e.StartByte != 2 {
		t.Errorf("edit StartByte = %d, want 2", e.StartByte)
	}
	if e.OldEndByte != 3 {
		t.Errorf("edit OldEndByte = %d, want 3", e.OldEndByte)
	}
	if e.NewEndByte != 4 {
		t.Errorf("edit NewEndByte = %d, want 4", e.NewEndByte)
	}
}

func TestRewriteMultipleNonOverlapping(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	source := []byte("1+2")
	tree := mustParse(t, parser, source)

	root := tree.RootNode()
	first := root.Child(0) // "1" or expression→NUMBER
	// Walk to a named NUMBER leaf.
	for first.ChildCount() > 0 {
		first = first.Child(0)
	}
	last := root.Child(root.ChildCount() - 1)

	rw := NewRewriter(source)
	rw.Replace(first, []byte("10"))
	rw.Replace(last, []byte("20"))
	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "10+20" {
		t.Errorf("newSource = %q, want %q", newSource, "10+20")
	}

	if len(edits) != 2 {
		t.Fatalf("expected 2 edits, got %d", len(edits))
	}
}

func TestRewriteInsertBefore(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	source := []byte("1+2")
	tree := mustParse(t, parser, source)

	root := tree.RootNode()
	last := root.Child(root.ChildCount() - 1)

	rw := NewRewriter(source)
	rw.InsertBefore(last, []byte("3+"))
	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "1+3+2" {
		t.Errorf("newSource = %q, want %q", newSource, "1+3+2")
	}

	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	// InsertBefore is a zero-width edit.
	e := edits[0]
	if e.OldEndByte != e.StartByte {
		t.Errorf("InsertBefore should be zero-width: StartByte=%d, OldEndByte=%d", e.StartByte, e.OldEndByte)
	}
}

func TestRewriteInsertAfter(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	source := []byte("1+2")
	tree := mustParse(t, parser, source)

	root := tree.RootNode()
	// Insert after the root — after the entire expression.
	rw := NewRewriter(source)
	rw.InsertAfter(root, []byte("+3"))
	newSource, _, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "1+2+3" {
		t.Errorf("newSource = %q, want %q", newSource, "1+2+3")
	}
}

func TestRewriteDelete(t *testing.T) {
	source := []byte("hello world")

	rw := NewRewriter(source)
	// Delete "world" at bytes [6, 11).
	rw.ReplaceRange(6, 11, nil)
	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "hello " {
		t.Errorf("newSource = %q, want %q", newSource, "hello ")
	}

	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	// NewEndByte should equal StartByte since content was deleted.
	if edits[0].NewEndByte != edits[0].StartByte {
		t.Errorf("delete: NewEndByte=%d, want StartByte=%d", edits[0].NewEndByte, edits[0].StartByte)
	}
}

func TestRewriteOverlappingEditsError(t *testing.T) {
	source := []byte("hello world")

	rw := NewRewriter(source)
	rw.ReplaceRange(0, 5, []byte("hi"))
	rw.ReplaceRange(3, 8, []byte("there"))
	_, _, err := rw.Apply()
	if err == nil {
		t.Fatal("expected error for overlapping edits, got nil")
	}
}

func TestRewriteOverlappingInsertionsError(t *testing.T) {
	source := []byte("hello")

	rw := NewRewriter(source)
	rw.ReplaceRange(3, 3, []byte("a"))
	rw.ReplaceRange(3, 3, []byte("b"))
	_, _, err := rw.Apply()
	if err == nil {
		t.Fatal("expected error for overlapping insertions at same point, got nil")
	}
}

func TestRewriteInvalidRangeReturnsError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end uint32
	}{
		{name: "end beyond source", start: 0, end: 6},
		{name: "start beyond source", start: 6, end: 6},
		{name: "reversed range", start: 4, end: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rw := NewRewriter([]byte("hello"))
			rw.ReplaceRange(tc.start, tc.end, []byte("x"))
			if _, _, err := rw.Apply(); err == nil {
				t.Fatal("Apply() succeeded for an invalid range")
			}
		})
	}
}

func TestRewriteAdjacentEdits(t *testing.T) {
	source := []byte("abcdef")

	rw := NewRewriter(source)
	rw.ReplaceRange(0, 3, []byte("AB"))  // "abc" → "AB"
	rw.ReplaceRange(3, 6, []byte("DEF")) // "def" → "DEF"
	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "ABDEF" {
		t.Errorf("newSource = %q, want %q", newSource, "ABDEF")
	}

	if len(edits) != 2 {
		t.Fatalf("expected 2 edits, got %d", len(edits))
	}
}

func TestRewriteApplyToTreeRoundTrip(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	source := []byte("1+2")
	tree := mustParse(t, parser, source)

	root := tree.RootNode()
	last := root.Child(root.ChildCount() - 1)

	rw := NewRewriter(source)
	rw.Replace(last, []byte("9"))
	newSource, err := rw.ApplyToTree(tree)
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "1+9" {
		t.Errorf("newSource = %q, want %q", newSource, "1+9")
	}

	// Incremental reparse should produce a valid tree.
	newTree := mustParseIncremental(t, parser, newSource, tree)
	newRoot := newTree.RootNode()
	if newRoot == nil {
		t.Fatal("incremental parse returned nil root")
	}

	newLast := newRoot.Child(newRoot.ChildCount() - 1)
	if newLast.Text(newSource) != "9" {
		t.Errorf("reparsed last child text = %q, want %q", newLast.Text(newSource), "9")
	}
}

func TestRewriteApplyMultiEditCoordinatesShift(t *testing.T) {
	source := []byte("a\nb\nc\n")
	rw := NewRewriter(source)
	rw.ReplaceRange(0, 1, []byte("A\nA"))
	rw.ReplaceRange(4, 5, []byte("C"))

	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if got, want := string(newSource), "A\nA\nb\nC\n"; got != want {
		t.Fatalf("newSource = %q, want %q", got, want)
	}
	if len(edits) != 2 {
		t.Fatalf("expected 2 edits, got %d", len(edits))
	}

	second := edits[1]
	if second.StartByte != 6 || second.OldEndByte != 7 || second.NewEndByte != 7 {
		t.Fatalf("second edit bytes = [%d,%d)->%d, want [6,7)->7", second.StartByte, second.OldEndByte, second.NewEndByte)
	}
	if second.StartPoint.Row != 3 || second.StartPoint.Column != 0 {
		t.Fatalf("second StartPoint = (%d,%d), want (3,0)", second.StartPoint.Row, second.StartPoint.Column)
	}
	if second.OldEndPoint.Row != 3 || second.OldEndPoint.Column != 1 {
		t.Fatalf("second OldEndPoint = (%d,%d), want (3,1)", second.OldEndPoint.Row, second.OldEndPoint.Column)
	}
	if second.NewEndPoint.Row != 3 || second.NewEndPoint.Column != 1 {
		t.Fatalf("second NewEndPoint = (%d,%d), want (3,1)", second.NewEndPoint.Row, second.NewEndPoint.Column)
	}
}

func TestRewriteApplyToTreeMultipleEditsNoDoubleShift(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	source := []byte("1+2+3")
	tree := mustParse(t, parser, source)

	rw := NewRewriter(source)
	rw.ReplaceRange(0, 1, []byte("10"))
	rw.ReplaceRange(4, 5, []byte("30"))

	newSource, err := rw.ApplyToTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(newSource), "10+2+30"; got != want {
		t.Fatalf("newSource = %q, want %q", got, want)
	}

	edits := tree.Edits()
	if len(edits) != 2 {
		t.Fatalf("expected 2 tree edits, got %d", len(edits))
	}
	if edits[1].StartByte != 5 || edits[1].OldEndByte != 6 || edits[1].NewEndByte != 7 {
		t.Fatalf("second tree edit bytes = [%d,%d)->%d, want [5,6)->7",
			edits[1].StartByte, edits[1].OldEndByte, edits[1].NewEndByte)
	}
	if edits[1].StartPoint.Row != 0 || edits[1].StartPoint.Column != 5 {
		t.Fatalf("second tree edit StartPoint = (%d,%d), want (0,5)",
			edits[1].StartPoint.Row, edits[1].StartPoint.Column)
	}

	incrTree := mustParseIncremental(t, parser, newSource, tree)
	if incrTree.RootNode() == nil {
		t.Fatal("incremental parse returned nil root")
	}
	fullTree := mustParse(t, parser, newSource)
	if got, want := incrTree.RootNode().Text(newSource), fullTree.RootNode().Text(newSource); got != want {
		t.Fatalf("incremental root text = %q, full root text = %q", got, want)
	}
}

func TestRewriteEmptyRewriter(t *testing.T) {
	source := []byte("unchanged")
	rw := NewRewriter(source)
	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "unchanged" {
		t.Errorf("newSource = %q, want %q", newSource, "unchanged")
	}

	if len(edits) != 0 {
		t.Errorf("expected 0 edits, got %d", len(edits))
	}
}

func TestRewriteReplaceWithSameText(t *testing.T) {
	source := []byte("abc")
	rw := NewRewriter(source)
	rw.ReplaceRange(0, 3, []byte("abc"))
	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "abc" {
		t.Errorf("newSource = %q, want %q", newSource, "abc")
	}

	// Edit still recorded even though text is the same.
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
}

func TestRewriteUnicode(t *testing.T) {
	source := []byte("hello 世界 end")
	// "世界" starts at byte 6, each rune is 3 bytes, so "世界" = bytes [6, 12).
	rw := NewRewriter(source)
	rw.ReplaceRange(6, 12, []byte("世"))
	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "hello 世 end" {
		t.Errorf("newSource = %q, want %q", newSource, "hello 世 end")
	}

	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
}

func TestRewriteMultilinePointCalculation(t *testing.T) {
	source := []byte("line1\nline2\nline3")
	// Replace "line2" (bytes 6-11) with "LINE\nTWO"
	rw := NewRewriter(source)
	rw.ReplaceRange(6, 11, []byte("LINE\nTWO"))
	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "line1\nLINE\nTWO\nline3" {
		t.Errorf("newSource = %q, want %q", newSource, "line1\nLINE\nTWO\nline3")
	}

	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}

	e := edits[0]
	// Start is at row 1, col 0 (first char of "line2").
	if e.StartPoint.Row != 1 || e.StartPoint.Column != 0 {
		t.Errorf("StartPoint = (%d,%d), want (1,0)", e.StartPoint.Row, e.StartPoint.Column)
	}
	// Old end: end of "line2" at row 1, col 5.
	if e.OldEndPoint.Row != 1 || e.OldEndPoint.Column != 5 {
		t.Errorf("OldEndPoint = (%d,%d), want (1,5)", e.OldEndPoint.Row, e.OldEndPoint.Column)
	}
	// New end: "LINE\nTWO" = row advances by 1 (one newline), col = 3.
	if e.NewEndPoint.Row != 2 || e.NewEndPoint.Column != 3 {
		t.Errorf("NewEndPoint = (%d,%d), want (2,3)", e.NewEndPoint.Row, e.NewEndPoint.Column)
	}
}

func TestRewriteDeleteNode(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	source := []byte("1+2")
	tree := mustParse(t, parser, source)

	root := tree.RootNode()
	last := root.Child(root.ChildCount() - 1)

	rw := NewRewriter(source)
	rw.Delete(last)
	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if string(newSource) != "1+" {
		t.Errorf("newSource = %q, want %q", newSource, "1+")
	}

	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
}

func TestRewriteReplaceRange(t *testing.T) {
	source := []byte("abcdef")
	rw := NewRewriter(source)
	rw.ReplaceRange(2, 4, []byte("XY"))
	newSource, _, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if string(newSource) != "abXYef" {
		t.Errorf("got %q, want %q", newSource, "abXYef")
	}
}

func TestRewriteEmptySource(t *testing.T) {
	source := []byte("")
	rw := NewRewriter(source)
	rw.ReplaceRange(0, 0, []byte("hello"))
	newSource, edits, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if string(newSource) != "hello" {
		t.Errorf("got %q, want %q", newSource, "hello")
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
}

func TestRewriteSourceNotMutated(t *testing.T) {
	source := []byte("original")
	orig := make([]byte, len(source))
	copy(orig, source)

	rw := NewRewriter(source)
	rw.ReplaceRange(0, 8, []byte("changed"))
	_, _, err := rw.Apply()
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(source, orig) {
		t.Errorf("source was mutated: got %q, want %q", source, orig)
	}
}
