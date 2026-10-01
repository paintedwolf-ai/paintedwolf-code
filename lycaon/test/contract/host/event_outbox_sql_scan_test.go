package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// mutatingQueryTables maps sqlc queries to written tables.
func mutatingQueryTables(dir string) (map[string][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		src := string(raw)
		heads := sqlcQueryName.FindAllStringSubmatchIndex(src, -1)
		for i, head := range heads {
			name := src[head[2]:head[3]]
			end := len(src)
			if i+1 < len(heads) {
				end = heads[i+1][0]
			}
			// Comments name tables in prose ("so an update can carry").
			body := sqlLineComment.ReplaceAllString(src[head[1]:end], "")
			seen := map[string]bool{}
			for _, match := range sqlcMutatingRe.FindAllStringSubmatch(body, -1) {
				table := strings.ToLower(match[1])
				// "UPDATE SET" inside an upsert names no table.
				if table == "set" || seen[table] {
					continue
				}
				seen[table] = true
				out[name] = append(out[name], table)
			}
		}
	}
	return out, nil
}

var (
	sqlcQueryName  = regexp.MustCompile(`(?m)^--\s*name:\s*(\w+)\s`)
	sqlcMutatingRe = regexp.MustCompile(`(?is)\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+([a-zA-Z_]\w*)`)
	sqlLineComment = regexp.MustCompile(`--[^\n]*`)
)

type sqlTrigger struct {
	name  string
	table string
	body  string
}

var triggerInsertInto = regexp.MustCompile(`(?is)\bINSERT\s+(?:OR\s+\w+\s+)?INTO\s+([a-zA-Z_]\w*)`)

// triggerInserts names the tables one trigger body inserts into.
func triggerInserts(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, match := range triggerInsertInto.FindAllStringSubmatch(body, -1) {
		table := strings.ToLower(match[1])
		if !seen[table] {
			seen[table] = true
			out = append(out, table)
		}
	}
	return out
}

var sqlTriggerHeader = regexp.MustCompile(`(?is)CREATE\s+TRIGGER\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s+(?:AFTER|BEFORE|INSTEAD\s+OF)\s+[\w\s,]*?\s+ON\s+(\w+)`)

// Trigger bodies end at the statement-terminating END.
func sqlTriggerBodies(schema string) []sqlTrigger {
	matches := sqlTriggerHeader.FindAllStringSubmatchIndex(schema, -1)
	out := make([]sqlTrigger, 0, len(matches))
	for _, m := range matches {
		name := schema[m[2]:m[3]]
		table := schema[m[4]:m[5]]
		body := schema[m[1]:]
		if end := strings.Index(body, "\nEND;"); end >= 0 {
			body = body[:end]
		}
		out = append(out, sqlTrigger{name: name, table: table, body: body})
	}
	return out
}
