package grammars

import (
	"fmt"
	"sync"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestParseFile(t *testing.T) {
	bt, err := ParseFile("main.go", []byte("package main\n\nfunc main() {}\n"))
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}
	defer bt.Release()

	root := bt.RootNode()
	if root == nil {
		t.Fatal("ParseFile returned nil root")
	}
	if got := bt.NodeType(root); got != "source_file" {
		t.Errorf("root type = %q, want %q", got, "source_file")
	}
}

func TestParseFileUnknownExtension(t *testing.T) {
	_, err := ParseFile("file.xyz", []byte("hello"))
	if err == nil {
		t.Fatal("expected error for unknown extension")
	}
}

func TestParseFileEmptySource(t *testing.T) {
	bt, err := ParseFile("main.go", []byte{})
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}
	defer bt.Release()
}

func TestParseFilePython(t *testing.T) {
	bt, err := ParseFile("script.py", []byte("def hello():\n    pass\n"))
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}
	defer bt.Release()

	if bt.RootNode() == nil {
		t.Fatal("ParseFile returned nil root for Python")
	}

	found := false
	gotreesitter.Walk(bt.RootNode(), func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
		if bt.NodeType(node) == "function_definition" {
			found = true
			return gotreesitter.WalkStop
		}
		return gotreesitter.WalkContinue
	})
	if !found {
		t.Error("expected to find function_definition in Python parse tree")
	}
}

func TestParseFilePooled(t *testing.T) {
	bt, err := ParseFilePooled("main.go", []byte("package main\n\nfunc main() {}\n"))
	if err != nil {
		t.Fatalf("ParseFilePooled error: %v", err)
	}
	defer bt.Release()
	root := bt.RootNode()
	if root == nil {
		t.Fatal("ParseFilePooled returned nil root")
	}
	if got := bt.NodeType(root); got != "source_file" {
		t.Errorf("root type = %q, want %q", got, "source_file")
	}
}

func TestParseFilePooledUnknownExtension(t *testing.T) {
	_, err := ParseFilePooled("file.xyz", []byte("hello"))
	if err == nil {
		t.Fatal("expected error for unknown extension")
	}
}

func TestParseFilePooledReusesPool(t *testing.T) {
	bt1, err := ParseFilePooled("a.go", []byte("package a\n"))
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	bt1.Release()
	bt2, err := ParseFilePooled("b.go", []byte("package b\n"))
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	bt2.Release()
}

func TestParseFilePooledConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			src := []byte(fmt.Sprintf("package p%d\n\nfunc f%d() {}\n", n, n))
			bt, err := ParseFilePooled(fmt.Sprintf("f%d.go", n), src)
			if err != nil {
				t.Errorf("concurrent parse %d: %v", n, err)
				return
			}
			bt.Release()
		}(i)
	}
	wg.Wait()
}
