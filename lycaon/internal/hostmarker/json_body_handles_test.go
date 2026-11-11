package hostmarker

import "testing"

func TestSplitToolJSONBodyKeepsEveryEvidenceHandle(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"one":  "[read#1]\n{\"status\":0}",
		"many": "[read#1]\n[read#2]\n{\"status\":0}",
	} {
		prefix, body, _, ok := SplitToolJSONBody(content)
		if !ok || body != "{\"status\":0}" {
			t.Fatalf("%s: body = %q, ok = %v", name, body, ok)
		}
		if prefix+body != content {
			t.Fatalf("%s: prefix = %q lost the handles", name, prefix)
		}
	}
}
