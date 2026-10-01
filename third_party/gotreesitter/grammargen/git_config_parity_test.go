package grammargen

import "testing"

func TestGitConfigIntegerValueParity(t *testing.T) {
	assertImportedDeepParityCases(t, "git_config", []struct {
		name string
		src  string
	}{
		{
			name: "repositoryformatversion_integer_zero",
			src:  "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n",
		},
	})
}
