package grammargen

import "testing"

func TestCommentSimpleTagOrderParity(t *testing.T) {
	assertImportedDeepParityCases(t, "comment", []struct {
		name string
		src  string
	}{
		{
			name: "simple_uppercase_tags",
			src: "\nTODO: something\n\nXXX: fix something else.\n\nTODO:    extra white spaces.\n\n" +
				"NOTAG:missing space\n\n(TODO: I'm inside parentheses)\n\nNOTE:\n\n" +
				"\"NOTE: this should work!\"\n\nDEBUG: this should work with symbols\n",
		},
	})
}
