package conformance_test

import (
	"slices"

	"github.com/lycaon/lycaon/test/commandconformance"
)

// Every g invocation exercises the catalog's three global Git option forms.
const gitFormPrelude = `g() { git --no-pager -C . -c color.ui=false "$@"; }; `

func gitForms(command string, options ...string) []string {
	keys := make([]string, 0, len(options)+3)
	for _, option := range append([]string{"-C", "-c", "--no-pager"}, options...) {
		keys = append(keys, command+":"+option)
	}
	return keys
}

var gitReadFormContracts = []contractCase{
	{
		Case: commandconformance.Case{
			ID: "git-grep-forms", Commands: []string{"git grep"}, Git: true,
			Forms:   gitForms("git grep", "-n", "-i", "-w", "-v", "-E", "-F", "-P", "-G", "-l", "-c", "-q", "-h", "-z", "--cached", "-e"),
			Program: gitFormPrelude + `for option in -n -i -w -v -E -F -P -G -l -c -q -h -z --cached; do g grep "$option" -e first -- sub/a.txt >/dev/null || exit 1; done; g grep -h -F -e first -- sub/a.txt`,
		},
		Stdout: "first\n",
	},
	{
		Case: commandconformance.Case{
			ID: "git-description-history-forms", Commands: []string{"git describe", "git reflog", "git shortlog"}, Git: true,
			Forms: slices.Concat(
				gitForms("git describe", "--tags", "--all", "--always", "--abbrev", "--long", "--match", "--exclude", "--first-parent", "--dirty"),
				gitForms("git reflog", "-n", "--date", "--oneline"), gitForms("git shortlog", "-s", "-n", "-e")),
			Program: gitFormPrelude + `g describe --tags --all --always --abbrev=7 --long --match 'v*' --exclude 'never*' --first-parent --dirty >/dev/null && g reflog -n 1 --date iso --oneline >/dev/null && g shortlog -s -n -e HEAD >/dev/null && printf 'history-ok\n'`,
		},
		Stdout: "history-ok\n",
	},
	{
		Case: commandconformance.Case{
			ID: "git-tree-object-forms", Commands: []string{"git ls-tree", "git cat-file"}, Git: true,
			Forms:   slices.Concat(gitForms("git ls-tree", "-r", "-t", "-d", "-l", "--name-only", "--name-status", "--full-tree", "-z", "--abbrev"), gitForms("git cat-file", "-t", "-s", "-p", "-e")),
			Program: gitFormPrelude + `for option in -r -t -d -l --name-only --name-status --full-tree -z; do g ls-tree "$option" --abbrev=7 HEAD >/dev/null || exit 1; done; for option in -t -s -p -e; do g cat-file "$option" HEAD >/dev/null || exit 1; done; g cat-file -p HEAD:sub/a.txt`,
		},
		Stdout: "first\nsecond\n",
	},
	{
		Case: commandconformance.Case{
			ID: "git-reference-forms", Commands: []string{"git show-ref", "git for-each-ref"}, Git: true,
			Forms:   slices.Concat(gitForms("git show-ref", "--heads", "--tags", "--verify", "-d", "-s", "--abbrev", "-q"), gitForms("git for-each-ref", "--format", "--sort", "--count", "--points-at", "--merged", "--no-merged", "--contains", "--no-contains")),
			Program: gitFormPrelude + `g show-ref --heads --tags -d -s --abbrev=7 >/dev/null && g show-ref --verify -q refs/heads/main && g for-each-ref --format '%(refname)' --sort refname --count 1 --points-at HEAD --merged=HEAD --contains=HEAD && g for-each-ref --no-merged=HEAD --no-contains=HEAD >/dev/null`,
		},
		Stdout: "refs/heads/main\n",
	},
}
