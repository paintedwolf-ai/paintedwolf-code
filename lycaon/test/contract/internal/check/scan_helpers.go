package check

import (
	"regexp"
	"strconv"
	"strings"
)

// Survey tool orderings that do not lead with find.
var LaneSSurveyStaleCopyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`read, grep, glob`),
	regexp.MustCompile(`read, glob,`),
	regexp.MustCompile(`\bor glob\b`),
}

func CheckPatterns(path, text string, patterns []*regexp.Regexp, skipLine func(trim string) bool) []string {
	var violations []string
	for i, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if skipLine != nil && skipLine(trim) {
			continue
		}
		for _, re := range patterns {
			if re.MatchString(line) {
				violations = append(violations, FmtLine(path, i+1, re.String(), trim))
			}
		}
	}
	return violations
}

func FmtLine(path string, line int, pattern, content string) string {
	return path + ":" + strconv.Itoa(line) + ": forbidden " + pattern + " in: " + content
}

func SkipCommentLine(trim string) bool {
	return strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "*") || trim == ""
}
