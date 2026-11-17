package structure

import "slices"

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

// Describe renders diagnostics as report lines.
func Describe(d *Diagnostics) []string { return d.describe() }
