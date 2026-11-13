package settings

import "testing"

func TestResearchToolsHaveNoPathWriteCrossing(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"web_search", "fetch_url"} {
		if IsPathMutatingTool(name) {
			t.Fatalf("%s must not declare a path write", name)
		}
	}
}
