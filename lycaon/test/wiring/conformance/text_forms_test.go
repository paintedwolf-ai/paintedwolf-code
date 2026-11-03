package conformance_test

import "github.com/lycaon/lycaon/test/commandconformance"

var textFormContracts = []contractCase{
	{
		Case: commandconformance.Case{
			ID: "text-cmp-forms", Commands: []string{"cmp"},
			Forms:   []string{"cmp:-s", "cmp:-l", "cmp:-b", "cmp:-n", "cmp:-i"},
			Program: `cmp -s sub/a.txt sub/a.txt && cmp -l -n 3 sub/a.txt sub/a.txt && cmp -b -i 1:1 -n 3 sub/a.txt sub/a.txt && printf 'equal\n'; cmp -s sub/a.txt sub/b.txt; printf 'different:%s\n' "$?"`,
		},
		Stdout: "equal\ndifferent:1\n",
	},
	{
		Case: commandconformance.Case{
			ID: "text-envsubst-forms", Commands: []string{"envsubst"}, Forms: []string{"envsubst:-v"},
			Program: `export FIRST=one SECOND=two; printf '$FIRST $SECOND\n' | envsubst '$FIRST'; envsubst -v '$FIRST ${SECOND}'`,
		},
		Stdout: "one $SECOND\nFIRST\nSECOND\n",
	},
	{
		Case: commandconformance.Case{
			ID: "text-iconv-forms", Commands: []string{"iconv"},
			Forms:   []string{"iconv:-f", "iconv:-t", "iconv:-c", "iconv:-l", "iconv:-o"},
			Program: `printf 'plain\n' | iconv -f UTF-8 -t ASCII -c -o converted.txt && cat converted.txt && iconv -l | grep -qi 'UTF-8' && printf 'encoding-listed\n'`,
			Tracked: []string{"converted.txt"},
		},
		Stdout: "plain\nencoding-listed\n", Files: map[string]string{"converted.txt": "plain\n"},
	},
	{
		Case: commandconformance.Case{
			ID: "text-od-forms", Commands: []string{"od"},
			Forms:   []string{"od:-A", "od:-t", "od:-N", "od:-j", "od:-v", "od:-c", "od:-x", "od:-b", "od:-d", "od:-o", "od:-s"},
			Program: `printf ABCD | od -A n -t x1 -N 2 -j 1 -v | tr -d ' \n'; printf '\n'; for option in -c -x -b -d -o -s; do printf ABCD | od "$option" >/dev/null || exit 1; done; printf 'formats-ok\n'`,
		},
		Stdout: "4243\nformats-ok\n",
	},
	{
		Case: commandconformance.Case{
			ID: "text-strings-forms", Commands: []string{"strings"},
			Forms:   []string{"strings:-n", "strings:-a", "strings:-f", "strings:-t", "strings:-e", "strings:-o"},
			Program: `strings -n 5 -a -f -t x -e s notes.txt >/dev/null && strings -o notes.txt >/dev/null && strings -n 5 notes.txt | head -n 1`,
		},
		Stdout: "alpha line 1\n",
	},
	{
		Case: commandconformance.Case{
			ID: "text-date-forms", Commands: []string{"date"},
			Forms:   []string{"date:-d", "date:-r", "date:-I", "date:-R", "date:--rfc-3339", "date:-j", "date:-f", "date:-v"},
			Program: `date -u -d @0 +%Y; date -u -r 0 +%Y; date -u -d 2025-01-02 -I; date -u -d 2025-01-02 -R; date -u -d 2025-01-02 --rfc-3339 seconds; date -u -j -f '%Y-%m-%d' 2025-01-02 -v +1d +%F`,
		},
		Stdout: "1970\n1970\n2025-01-02\nThu, 02 Jan 2025 00:00:00 +0000\n2025-01-02 00:00:00+00:00\n2025-01-03\n",
	},
}
