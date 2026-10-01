//go:build grammar_subset && grammar_subset_arduino

package grammars

func init() {
	RegisterExternalScanner("arduino", ArduinoExternalScanner{})
}
