package conformance_test

import "github.com/lycaon/lycaon/test/commandconformance"

var shellFormContracts = []contractCase{
	{
		Case: commandconformance.Case{
			ID: "shell-timeout-nohup-forms", Commands: []string{"timeout", "nohup"},
			Forms:   []string{"timeout:-s", "timeout:-k", "timeout:--preserve-status", "timeout:--foreground", "timeout:-v"},
			Program: `timeout -s TERM -k 1 --preserve-status --foreground -v 5 printf 'timeout-ok\n' && nohup printf 'nohup-ok\n'`,
		},
		Stdout: "timeout-ok\nnohup-ok\n",
	},
	{
		Case: commandconformance.Case{
			ID: "shell-limit-query-forms", Commands: []string{"ulimit"},
			Forms:   []string{"ulimit:-S", "ulimit:-H", "ulimit:-a", "ulimit:-c", "ulimit:-f", "ulimit:-n", "ulimit:-s", "ulimit:-t", "ulimit:-u", "ulimit:-v"},
			Program: `ulimit -S -n >/dev/null && ulimit -H -n >/dev/null && ulimit -a >/dev/null || exit 1; for option in -c -f -n -s -t -u -v; do ulimit "$option" >/dev/null || exit 1; done; printf 'limits-ok\n'`,
		},
		Stdout: "limits-ok\n",
	},
	{
		Case: commandconformance.Case{
			ID: "shell-read-deadline", Commands: []string{"read"}, Forms: []string{"read:-t"},
			Program: `IFS= read -r -t 1 line <<< 'available now' && printf '%s\n' "$line"`,
		},
		Stdout: "available now\n",
	},
	{
		Case: commandconformance.Case{
			ID: "shell-lsof-format-forms", Commands: []string{"lsof"}, Forms: []string{"lsof:-t", "lsof:-n", "lsof:-P"},
			Program: `lsof -t -n -P >/dev/null; status=$?; test "$status" -le 1 && printf 'listing-ok\n'`,
		},
		Stdout: "listing-ok\n", Stderr: "lsof: only this session's listeners are listed; name a port with -i :PORT to include other processes\n",
	},
}
