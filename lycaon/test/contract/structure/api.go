package structure

import "slices"

// Exported API for report consumers, mainly the maintainability budget suite.
// The contract tests in this package use the unexported forms.

// Declaration and Declarations are the exported views of one directory's
// character and the declaration document.
type Declaration = declaration
type Declarations = declarations

// Diagnostics is the exported view of one directory's structural evidence.
type Diagnostics = diagnostics

// BuildGraph parses the production import graph of the checkout at tree.
func BuildGraph(tree Tree) (*Graph, error) { return buildGraph(tree) }

// LoadDeclarations reads and validates declarations through tree.Read.
func LoadDeclarations(tree Tree) (Declarations, error) {
	return loadDeclarations(tree.Read)
}

// DeclaredNames lists the declared directories in sorted order.
func DeclaredNames(d Declarations) []string {
	names := make([]string, 0, len(d))
	for name := range d {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Analyze computes the diagnostics for one declared directory.
func Analyze(graph *Graph, key string, d Declaration) (*Diagnostics, error) {
	return analyze(graph, key, d)
}

// Describe renders diagnostics as report lines.
func Describe(d *Diagnostics) []string { return d.describe() }