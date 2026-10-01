//go:build grammar_subset && grammar_subset_lua

package grammars

func init() {
	RegisterExternalScanner("lua", LuaExternalScanner{})
	RegisterExternalLexStates("lua", luaExternalLexStates)
}
