package gotreesitter_test

import (
	"bytes"
	"os"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestIssue490GoGrammarRegression(t *testing.T) {
	data, err := os.ReadFile("internal/parsercorephase0/core_test.go")
	if err != nil {
		t.Fatal(err)
	}
	const fragmentBytes, repeats = 8328, 8
	start := bytes.Index(data, []byte("func TestReduceOutputsAggregatesFreshnessPerFinalBoundary"))
	if start < 0 {
		t.Fatal("Go regression fixture marker is absent")
	}
	end := start + fragmentBytes
	if len(data) < end {
		t.Fatalf("fixture bytes after marker = %d, want at least %d", len(data)-start, fragmentBytes)
	}
	source := append([]byte("package p\n"), data[start:end]...)
	fragment := append([]byte(nil), data[start:end]...)
	for i := 1; i < repeats; i++ {
		source = append(source, fragment...)
	}

	tree, err := gotreesitter.NewParser(grammars.GoLanguage()).
		Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Release)

	if tree.ParseStoppedEarly() {
		t.Fatalf("Go parse stopped early: %s", tree.ParseRuntime().Summary())
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("Go parse returned no root")
	}
	if root.HasError() {
		t.Fatal("Go parse returned an error tree")
	}
	if root.EndByte() != uint32(len(source)) {
		t.Fatalf("Go root ends at %d, want %d", root.EndByte(), len(source))
	}
	if !publicResultTreeAcyclic(tree.RootNode()) {
		t.Fatal("Go parse returned an invalid result tree")
	}
}

func publicResultTreeAcyclic(root *gotreesitter.Node) bool {
	const (
		gray  = 1
		black = 2
	)
	color := make(map[*gotreesitter.Node]uint8)
	var visit func(*gotreesitter.Node) bool
	visit = func(node *gotreesitter.Node) bool {
		if node == nil {
			return true
		}
		switch color[node] {
		case gray:
			return false
		case black:
			return true
		}
		color[node] = gray
		for i := 0; i < node.ChildCount(); i++ {
			if !visit(node.Child(i)) {
				return false
			}
		}
		color[node] = black
		return true
	}
	return visit(root)
}
