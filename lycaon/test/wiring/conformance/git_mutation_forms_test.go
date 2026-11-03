package conformance_test

import (
	"slices"

	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

const gitTextChangePatch = "diff --git a/sub/a.txt b/sub/a.txt\nindex 66a52ee7a1d803dc57859c3e95ac9dcdc87c0164..1b42a2f5382e44f10eee3c02f7ce30646fed291a 100644\n" + textChangePatch

var gitMutationFormContracts = []contractCase{
	{
		Case: commandconformance.Case{
			ID: "git-apply-forms", Commands: []string{"git apply"}, Git: true,
			Forms:   gitForms("git apply", "--check", "--stat", "--numstat", "--summary", "-p", "-R", "--index", "--cached", "-3", "--whitespace", "--include", "--exclude", "--directory", "-v"),
			Program: gitFormPrelude + `g apply --check change.patch && g apply --stat change.patch >/dev/null && g apply --numstat change.patch >/dev/null && g apply --summary change.patch >/dev/null && g apply -p 1 --whitespace=nowarn --include=sub/a.txt --exclude=never --directory=. -v change.patch 2>/dev/null && test "$(head -n 1 sub/a.txt)" = changed && g apply -R change.patch && g apply --index change.patch && g apply --index -R change.patch && g apply --cached change.patch && g apply --cached -R change.patch && g apply -3 change.patch 2>/dev/null && g apply --index -R change.patch && cat sub/a.txt`,
			Files:   map[string]referenceuserland.Entry{"change.patch": {Data: []byte(gitTextChangePatch)}},
			Tracked: []string{"sub/a.txt"},
		},
		Stdout: "first\nsecond\n", Files: map[string]string{"sub/a.txt": "first\nsecond\n"},
	},
	{
		Case: commandconformance.Case{
			ID: "git-move-remove-forms", Commands: []string{"git mv", "git rm"}, Git: true,
			Forms:   slices.Concat(gitForms("git mv", "-f", "-k", "-n", "-v"), gitForms("git rm", "-r", "--cached", "-f", "-n", "-q", "--ignore-unmatch")),
			Program: gitFormPrelude + `g mv -n -v sub/a.txt sub/renamed.txt >/dev/null && test -f sub/a.txt && g mv -f -k -v sub/a.txt sub/renamed.txt >/dev/null && test ! -e sub/a.txt && test -f sub/renamed.txt && g rm -n -f -q --ignore-unmatch sub/renamed.txt >/dev/null && test -f sub/renamed.txt && g rm -r --cached -f -q sub/renamed.txt && test -f sub/renamed.txt && g add sub/renamed.txt && g rm -r -f -q --ignore-unmatch sub/renamed.txt && test ! -e sub/renamed.txt && printf 'git-paths-ok\n'`,
			Tracked: []string{"sub/a.txt", "sub/renamed.txt"},
		},
		Stdout: "git-paths-ok\n",
	},
	{
		Case: commandconformance.Case{
			ID: "git-bisect-forms", Git: true,
			Commands: []string{"git bisect", "git bisect start", "git bisect skip", "git bisect run", "git bisect reset", "git bisect log", "git bisect terms", "git bisect visualize", "git bisect view"},
			Forms:    slices.Concat(gitForms("git bisect"), gitForms("git bisect start", "--term-old", "--term-new"), gitForms("git bisect skip"), gitForms("git bisect run"), gitForms("git bisect reset"), gitForms("git bisect log"), gitForms("git bisect terms", "--term-old", "--term-new"), gitForms("git bisect visualize", "--oneline"), gitForms("git bisect view", "--oneline")),
			Program:  gitFormPrelude + `for message in second third fourth; do printf '%s\n' "$message" > step.txt && g add step.txt && g commit -q -m "$message" || exit 1; done; newest=$(g rev-parse HEAD); oldest=$(g rev-parse HEAD~3); g bisect start --term-old=good --term-new=bad "$newest" "$oldest" >/dev/null && test "$(g bisect terms --term-old)" = good && test "$(g bisect terms --term-new)" = bad && g bisect log >/dev/null && g bisect visualize --oneline >/dev/null && g bisect view --oneline >/dev/null && g bisect good "$oldest" >/dev/null && g bisect skip >/dev/null && g bisect reset >/dev/null 2>/dev/null && g bisect start "$newest" "$oldest" >/dev/null && g bisect run printf good >/dev/null && g bisect reset >/dev/null 2>/dev/null && test "$(g rev-parse --abbrev-ref HEAD)" = main && printf 'bisect-ok\n'`,
		},
		Stdout: "bisect-ok\n",
	},
}
