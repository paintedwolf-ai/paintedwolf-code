package settings

import "testing"

// A fetch destination crosses the file-write approval boundary.
func TestFetchURLDeclaresAWriteCrossing(t *testing.T) {
	t.Parallel()
	if !IsPathMutatingTool("fetch_url") {
		t.Fatal("fetch_url is not on the path-mutating axis, so `dest` writes classify as reads")
	}
}

func TestWebSearchDeclaresNoPathCrossing(t *testing.T) {
	t.Parallel()
	if IsPathMutatingTool("web_search") {
		t.Fatal("web_search declares no path and must not be path-mutating")
	}
}
