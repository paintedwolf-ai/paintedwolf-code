package commandconformance

var languageCases = []Case{
	{
		ID:          "lang-quoting-single-and-double",
		Description: "Single quotes preserve metacharacters; double quotes expand variables and escapes without splitting.",
		Commands:    []string{"printf"},
		Program:     `x='world  wide'; printf '<%s>\n' 'a; rm -rf / | $x *' "hello $x \"quoted\" \$lit" escaped\ space`,
		References:  allUserlands,
	},
	{
		ID:          "lang-word-splitting-and-empty",
		Description: "Unquoted expansions split on default IFS whitespace and drop empty words; quoted expansions stay intact.",
		Commands:    []string{"printf"},
		Program:     `x='  one   two	three  '; empty=''; printf '<%s>\n' $empty "$empty" $x "$x" `,
		References:  allUserlands,
	},
	{
		ID:          "lang-glob-sorting-and-brackets",
		Description: "Globs expand in deterministic byte order, support ? and [...], and skip dotfiles unless prefixed.",
		Commands:    []string{"printf"},
		Program:     `printf '%s\n' sub/*.txt sub/[a-b].txt sub/?.txt .hidden* 'sub/*.txt' sub/*.nonexistent`,
		References:  allUserlands,
	},
	{
		ID:          "lang-globstar-recursive",
		Description: "Recursive ** globstar matches nested files in deterministic order.",
		Commands:    []string{"printf"},
		Program:     `printf '%s\n' **/*.go`,
		References:  bashUserlands,
	},
	{
		ID:          "lang-command-substitutions",
		Description: "Nested $(...) and backtick command substitutions strip trailing newlines without reparsing output.",
		Commands:    []string{"printf", "cat"},
		Program:     "a=$(printf 'hello\\n\\n'); b=`cat sub/a.txt | head -n 1`; printf '%s:%s\\n' \"$a\" \"$b\" ",
		References:  allUserlands,
	},
	{
		ID:          "lang-parameter-operators",
		Description: "Parameter expansion default, alternative, length, and prefix/suffix trim operators.",
		Commands:    []string{"printf"},
		Program:     `v=dir/sub/file.tar.gz; e=''; printf '%s|%s|%s|%s|%s|%s|%s|%s|%s\n' "${unset_var:-def}" "${e:-def2}" "${e-empty_ok}" "${v:+present}" "${#v}" "${v#*/}" "${v##*/}" "${v%.*}" "${v%%.*}" `,
		References:  allUserlands,
	},
	{
		ID:          "lang-statuses-and-short-circuit",
		Description: "Boolean built-ins, negation, &&, ||, and sequential status propagation.",
		Commands:    []string{"true", "false", ":", "printf"},
		Program:     `true && printf 'and:0\n'; false || printf 'or:%d\n' "$?"; false && printf 'never\n'; ! false && : && printf 'neg:%d\n' "$?" `,
		References:  allUserlands,
	},
	{
		ID:          "lang-pipeline-and-early-consumer",
		Description: "Multi-stage pipeline and early consumer stop settle with the rightmost stage status.",
		Commands:    []string{"cat", "head", "tr", "printf"},
		Forms:       []string{"head:-n", "tr:-c", "tr:-t"},
		Program:     `cat notes.txt | tr 'a-z' 'A-Z' | head -n 2; false | true; printf 'pipe=%d\n' "$?" `,
		References:  allUserlands,
	},
	{
		ID:          "lang-loops-for-while-break-continue",
		Description: "For and while loops with break and continue.",
		Commands:    []string{"printf", "[", "test", "break", "continue"},
		Program:     `for w in one two three four; do if [ "$w" = two ]; then continue; fi; if [ "$w" = four ]; then break; fi; printf 'for:%s\n' "$w"; done; acc=x; while test "$acc" != xxxx; do printf 'while:%s\n' "$acc"; acc="${acc}x"; done`,
		References:  allUserlands,
	},
	{
		ID:          "lang-conditionals-and-predicates",
		Description: "If/elif/else with test, [, and [[ string/glob/regex/file predicates.",
		Commands:    []string{"test", "[", "[["},
		Program:     `if test -f missing.txt; then printf 'bad\n'; elif [ -f notes.txt ] && [ -d sub ] && [[ "alpha_1" == alpha_* ]] && [[ "beta42" =~ ^beta[0-9]+$ ]]; then printf 'cond-ok\n'; else printf 'else\n'; fi`,
		References:  bashUserlands,
	},
	{
		ID:          "lang-test-nonempty-file-predicate",
		Description: "POSIX test/[ -s non-empty file predicate.",
		Commands:    []string{"test", "["},
		Program:     `[ -s notes.txt ] && test ! -s empty.txt && printf 'size-ok\n'`,
		References:  allUserlands,
	},
	{
		ID:          "lang-functions-and-frames",
		Description: "Shell functions, positional parameters, return statuses, brace groups vs subshells.",
		Commands:    []string{"printf", "export", "unset", "return", "exit", "set"},
		Program:     `fn() { printf 'fn:%s:%d\n' "$1" "$#"; return 4; }; fn 'arg one' two || printf 'ret=%d\n' "$?"; v=outer; { v=brace; }; ( v=subshell ); export EXP=1; unset EXP; printf 'v=%s exp=%s\n' "$v" "${EXP:-unset}" `,
		References:  allUserlands,
	},
	{
		ID:          "lang-heredocs-literal-expanded-tab",
		Description: "Literal <<'EOF', expanded <<EOF, and tab-stripped <<-EOF here-documents.",
		Commands:    []string{"cat"},
		Program: `v=val
cat <<'EOF'
lit:$v
EOF
cat <<EOF
exp:$v
EOF
cat <<-'EOF'
	stripped
	EOF
`,
		References: allUserlands,
	},
	{
		ID:          "lang-redirects-and-streams",
		Description: "Redirects (>, >>, <, 2>&1, 2>/dev/null) and pipeline output redirect.",
		Commands:    []string{"printf", "cat", "tr", "ls"},
		Forms:       []string{"ls:-p", "ls:-i", "ls:-s", "ls:-n", "ls:-L", "ls:-U", "ls:-c", "ls:-u", "ls:--group-directories-first", "ls:--color", "ls:-G", "ls:-o", "ls:-g", "ls:-f", "ls:-k", "ls:-m", "ls:-H", "ls:-P"},
		Program:     `printf 'first-out\n' > out.txt; printf 'second-out\n' >> out.txt; cat < out.txt; ls missing_target 2>/dev/null || printf 'masked=%d\n' "$?"; ( printf 'out-line\n'; printf 'err-line\n' >&2 ) > merged.txt 2>&1; cat sub/a.txt | tr 'a-z' 'A-Z' > sub/a_upper.txt && cat sub/a_upper.txt`,
		Tracked:     []string{"out.txt", "merged.txt", "sub/a_upper.txt"},
		References:  allUserlands,
	},
}
