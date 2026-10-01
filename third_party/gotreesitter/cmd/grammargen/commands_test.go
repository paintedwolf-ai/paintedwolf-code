package main

import (
	"reflect"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestNormalizeSubcommandArgsAllowsGrammarBeforeFlags(t *testing.T) {
	got := normalizeSubcommandArgs([]string{"calc", "-text", "1+2", "-runtime"})
	want := []string{"-text", "1+2", "-runtime", "calc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeSubcommandArgs() = %#v, want %#v", got, want)
	}
}

func TestNormalizeSubcommandArgsKeepsFlagEqualsValues(t *testing.T) {
	got := normalizeSubcommandArgs([]string{"calc", "-text=1+2", "-sample", "sample.txt"})
	want := []string{"-text=1+2", "-sample", "sample.txt", "calc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeSubcommandArgs() = %#v, want %#v", got, want)
	}
}

func TestNormalizeSubcommandArgsHandlesAuthoringValueFlags(t *testing.T) {
	got := normalizeSubcommandArgs([]string{
		"calc",
		"-format", "json",
		"-expect", "want.sexpr",
		"-write-expect", "got.sexpr",
		"-json-out", "grammar.json",
		"-js-cli", "grammar.js",
		"-conflicts", "2",
	})
	want := []string{
		"-format", "json",
		"-expect", "want.sexpr",
		"-write-expect", "got.sexpr",
		"-json-out", "grammar.json",
		"-js-cli", "grammar.js",
		"-conflicts", "2",
		"calc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeSubcommandArgs() = %#v, want %#v", got, want)
	}
}

func TestParseResultFailedRejectsMissingNode(t *testing.T) {
	lang := grammars.CLanguage()
	tree, err := gotreesitter.NewParser(lang).Parse([]byte("int value"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil || !root.HasErrorOrMissing() {
		t.Fatalf("expected recovery node, got %v", root)
	}
	if !parseResultFailed(parseResult{tree: tree, root: root}) {
		t.Fatal("parseResultFailed accepted a tree with a missing node")
	}
}
