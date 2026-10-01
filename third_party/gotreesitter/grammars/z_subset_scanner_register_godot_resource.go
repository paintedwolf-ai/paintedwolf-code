//go:build grammar_subset && grammar_subset_godot_resource

package grammars

func init() {
	RegisterExternalScanner("godot_resource", GodotResourceExternalScanner{})
}
