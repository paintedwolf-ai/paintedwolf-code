package wirespec

import (
	"regexp"
	"strings"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func SchemaContainsEnumSet(sql, table string, column string, want []string) bool {
	// Match CHECK on the given column within the table block.
	tableRE := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS ` + regexp.QuoteMeta(table) + ` \((.*?)\);`)
	m := tableRE.FindStringSubmatch(sql)
	if m == nil {
		return false
	}
	block := m[1]
	colRE := regexp.MustCompile(regexp.QuoteMeta(column) + `\s+TEXT[^)]*CHECK\s*\([^)]*IN\s*\(([^)]+)\)`)
	cm := colRE.FindStringSubmatch(block)
	if cm == nil {
		// Multi-line CHECK (status columns often span lines).
		colRE = regexp.MustCompile(regexp.QuoteMeta(column) + `\s+TEXT[\s\S]*?CHECK\s*\(\s*` + regexp.QuoteMeta(column) + `\s+IN\s*\(([^)]+)\)`)
		cm = colRE.FindStringSubmatch(block)
	}
	if cm == nil {
		return false
	}
	got := parseSQLInList(cm[1])
	return contractcheck.SortedSetEqual(want, got)
}

func parseSQLInList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "'")
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
