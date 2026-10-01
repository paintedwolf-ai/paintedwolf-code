package wirespec

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

type openAPIPathsDoc struct {
	Paths map[string]map[string]openAPIOperation `yaml:"paths"`
}

type openAPIOperation struct {
	OperationID  string               `yaml:"operationId"`
	LycaonStatus string               `yaml:"x-paintedwolf-status"`
	Responses    map[string]yaml.Node `yaml:"responses"`
}

type openAPIRoute struct {
	Method             string
	Path               string
	OperationID        string
	Stub               bool
	DocumentedStatuses []string
}

func LoadOpenAPIRoutes(repoRoot string) ([]openAPIRoute, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "docs", "openapi.yaml"))
	if err != nil {
		return nil, err
	}
	var doc openAPIPathsDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var routes []openAPIRoute
	for path, methods := range doc.Paths {
		for method, op := range methods {
			method = strings.ToUpper(method)
			if method == "PARAMETERS" || method == "SERVERS" {
				continue
			}
			var statuses []string
			for code := range op.Responses {
				statuses = append(statuses, code)
			}
			routes = append(routes, openAPIRoute{
				Method:             method,
				Path:               path,
				OperationID:        op.OperationID,
				Stub:               op.LycaonStatus == "stub",
				DocumentedStatuses: statuses,
			})
		}
	}
	return routes, nil
}

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
