package survey

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"regexp"
	"regexp/syntax"
	"strings"
)

func compileGrepPattern(opts grepOptions) (*regexp.Regexp, error) {
	if opts.structural {
		return nil, nil
	}
	re, err := compileGrepRegex(opts.pattern, opts.caseInsensitive)
	if err != nil {
		var toolReject *toolrejection.ToolReject
		if errors.As(err, &toolReject) {
			return nil, toolReject
		}
		return nil, safecmd.Reject("GREP_REGEX_INVALID", map[string]any{
			"pattern": opts.pattern,
			"detail":  err.Error(),
		})
	}
	return re, nil
}

// unescapedRegexOperators lists unescaped RE2 operators in first-seen order, backtick-quoted.
func unescapedRegexOperators(pattern string) string {
	const operators = ".*+?()[]{}|^$"
	var found []string
	seen := map[rune]bool{}
	escaped := false
	for _, r := range pattern {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if strings.ContainsRune(operators, r) && !seen[r] {
			seen[r] = true
			found = append(found, "`"+string(r)+"`")
		}
	}
	return strings.Join(found, ", ")
}

func compileGrepRegex(pattern string, caseInsensitive bool) (*regexp.Regexp, error) {
	flags := syntax.Perl
	if caseInsensitive {
		flags |= syntax.FoldCase
	}
	parsed, err := syntax.Parse(pattern, flags)
	if err != nil {
		return nil, err
	}
	if regexHasNestedRepeat(parsed) {
		return nil, safecmd.Reject("GREP_MATCH_BUDGET", map[string]any{
			"pattern":            pattern,
			"detail":             "nested quantifiers exceed safe budget",
			"grep_nested_repeat": true,
			"max_len":            safecmd.GrepMaxPatternLen,
		})
	}
	compiled, err := regexp.Compile(parsed.String())
	if err != nil {
		return nil, err
	}
	return compiled, nil
}

func regexHasNestedRepeat(re *syntax.Regexp) bool {
	return regexNestedRepeat(re, false)
}

func regexNestedRepeat(re *syntax.Regexp, insideHeavyRepeat bool) bool {
	if re == nil {
		return false
	}
	heavy := isHeavyRepeatOp(re.Op)
	if insideHeavyRepeat && heavy {
		return true
	}
	nextInside := insideHeavyRepeat || heavy
	for _, sub := range re.Sub {
		if regexNestedRepeat(sub, nextInside) {
			return true
		}
	}
	return false
}

func isHeavyRepeatOp(op syntax.Op) bool {
	switch op {
	case syntax.OpStar, syntax.OpPlus, syntax.OpRepeat:
		return true
	default:
		return false
	}
}
