package gotreesitter

import (
	"bytes"
	"testing"
)

func TestNormalizeDTDElementDeclRecoveredPEReference(t *testing.T) {
	const (
		symName Symbol = iota + 1
		symContentSpec
		symPEReference
		symPercent
		symSemi
		symElementDecl
	)
	lang := &Language{
		Name: "dtd",
		SymbolNames: []string{
			"EOF",
			"Name",
			"contentspec",
			"PEReference",
			"%",
			";",
			"elementdecl",
		},
		SymbolMetadata: []SymbolMetadata{
			{},
			{Name: "Name", Visible: true, Named: true},
			{Name: "contentspec", Visible: true, Named: true},
			{Name: "PEReference", Visible: true, Named: true},
			{Name: "%", Visible: true, Named: false},
			{Name: ";", Visible: true, Named: false},
			{Name: "elementdecl", Visible: true, Named: true},
		},
	}
	source := []byte("<!ELEMENT colspec %ho; EMPTY >")
	nameStart := uint32(bytes.Index(source, []byte("colspec")))
	peEnd := uint32(bytes.Index(source, []byte(";"))) + 1
	emptyStart := uint32(bytes.Index(source, []byte("EMPTY")))
	emptyEnd := emptyStart + uint32(len("EMPTY"))

	recovered := newLeafNodeInArena(nil, errorSymbol, true, nameStart, peEnd,
		advancePointByBytes(Point{}, source[:nameStart]),
		advancePointByBytes(Point{}, source[:peEnd]))
	recovered.setHasError(true)
	empty := newLeafNodeInArena(nil, symName, true, emptyStart, emptyEnd,
		advancePointByBytes(Point{}, source[:emptyStart]),
		advancePointByBytes(Point{}, source[:emptyEnd]))
	bogusContentSpec := newLeafNodeInArena(nil, symContentSpec, true, emptyEnd+1, emptyEnd+2,
		advancePointByBytes(Point{}, source[:emptyEnd+1]),
		advancePointByBytes(Point{}, source[:emptyEnd+2]))
	element := newParentNodeInArena(nil, symElementDecl, true, []*Node{recovered, empty, bogusContentSpec}, nil, 0)

	normalizeDTDCompatibility(element, source, lang)

	if got, want := resultChildCount(element), 3; got != want {
		t.Fatalf("element child count = %d, want %d", got, want)
	}
	if got := resultChildAt(element, 0).Type(lang); got != "Name" {
		t.Fatalf("child[0] type = %q, want Name", got)
	}
	contentSpec := resultChildAt(element, 1)
	if got := contentSpec.Type(lang); got != "contentspec" {
		t.Fatalf("child[1] type = %q, want contentspec", got)
	}
	peRef := resultChildAt(contentSpec, 0)
	if got := peRef.Type(lang); got != "PEReference" {
		t.Fatalf("contentspec child type = %q, want PEReference", got)
	}
	errNode := resultChildAt(element, 2)
	if got := errNode.Type(lang); got != "ERROR" {
		t.Fatalf("child[2] type = %q, want ERROR", got)
	}
	if got, want := resultChildCount(errNode), 1; got != want {
		t.Fatalf("ERROR child count = %d, want %d", got, want)
	}
	if got := resultChildAt(errNode, 0).Type(lang); got != "Name" {
		t.Fatalf("ERROR child type = %q, want Name", got)
	}
}
