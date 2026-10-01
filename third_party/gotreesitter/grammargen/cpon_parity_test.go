package grammargen

import "testing"

func TestCponEscapedBlobDeepParity(t *testing.T) {
	assertImportedDeepParityCases(t, "cpon", []struct {
		name string
		src  string
	}{
		{name: "escaped_blob_octal_prefix", src: "[\n  b\"ab\\\\\\3123\" // \"ab\\123\"\n  b\"ab1\"\n  b\"ab\\31\"\n  x\"616231\"\n]\n"},
	})
}
