package conformance_test

import "github.com/lycaon/lycaon/test/commandconformance"

var pathFormContracts = []contractCase{
	{Case: commandconformance.Case{ID: "find-observes-created-file", Commands: []string{"find"}, Program: `find sub -print >/dev/null; cp sub/a.txt sub/del.tmp && find sub -name 'del.tmp' -print -delete && test ! -e sub/del.tmp && printf 'removed\n'`, Tracked: []string{"sub/del.tmp"}}, Stdout: "sub/del.tmp\nremoved\n"},
	{
		Case: commandconformance.Case{
			ID: "paths-basename-forms", Commands: []string{"basename"},
			Forms:   []string{"basename:-s", "basename:-a", "basename:-z"},
			Program: `basename -s .txt sub/a.txt; basename -a sub/a.txt sub/b.txt; basename -z sub/a.txt`,
		},
		Stdout: "a\na.txt\nb.txt\na.txt\x00",
	},
	{
		Case: commandconformance.Case{
			ID: "paths-realpath-readlink-forms", Commands: []string{"realpath", "readlink", "ln"},
			Forms:   []string{"realpath:-e", "realpath:-m", "realpath:-s", "realpath:-q", "realpath:-z", "realpath:--relative-to", "realpath:--relative-base", "readlink:-e", "readlink:-m", "ln:-s", "ln:-f", "ln:-n", "ln:-v", "ln:-r"},
			Program: `ln -s -f -n -v -r sub/a.txt soft >/dev/null && test "$(readlink soft)" = sub/a.txt && test "$(realpath -e -s -q --relative-to . --relative-base . soft)" = soft && realpath -m -z absent >/dev/null && test "$(readlink -e soft)" = "$(realpath sub/a.txt)" && readlink -m absent >/dev/null && cat soft`,
		},
		Stdout: "first\nsecond\n",
	},
	{
		Case: commandconformance.Case{
			ID: "paths-find-newer-forms", Commands: []string{"find"},
			Forms:   []string{"find:-neweraa", "find:-neweram", "find:-newerac", "find:-newerat", "find:-newerma", "find:-newermm", "find:-newermc", "find:-newermt", "find:-newerca", "find:-newercm", "find:-newercc", "find:-newerct"},
			Program: `for option in -neweraa -neweram -newerac -newerma -newermm -newermc -newerca -newercm -newercc; do find sub/a.txt "$option" sub/a.txt >/dev/null || exit 1; done; for option in -newerat -newermt -newerct; do find sub/a.txt "$option" '2020-01-01' >/dev/null || exit 1; done; printf 'timestamps-ok\n'`,
		},
		Stdout: "timestamps-ok\n",
	},
}
