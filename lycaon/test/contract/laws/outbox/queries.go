package outbox

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/pkg/testcorpus"
	"github.com/lycaon/lycaon/test/contract/laws"
)

// mutatingQueryTables maps sqlc queries to the tables they write.
func mutatingQueryTables(files []testcorpus.File) (map[string][]string, error) {
	out := map[string][]string{}
	for _, file := range files {
		src := file.Text()
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
	if len(out) == 0 {
		return nil, fmt.Errorf("no sqlc queries")
	}
	return out, nil
}

var (
	sqlcQueryName  = regexp.MustCompile(`(?m)^--\s*name:\s*(\w+)\s`)
	sqlcMutatingRe = regexp.MustCompile(`(?is)\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+([a-zA-Z_]\w*)`)
	sqlLineComment = regexp.MustCompile(`--[^\n]*`)
)

// Trigger is one trigger of the live schema.
type Trigger struct {
	Name, Table, SQL string
}

// Triggers lists the live schema's triggers.
func Triggers(r *laws.Run) []Trigger {
	rows, err := r.Schema().Query(`SELECT name, tbl_name, sql FROM sqlite_schema WHERE type = 'trigger' ORDER BY name`)
	r.Must(err)
	defer func() { _ = rows.Close() }()
	var out []Trigger
	for rows.Next() {
		var t Trigger
		r.Must(rows.Scan(&t.Name, &t.Table, &t.SQL))
		out = append(out, t)
	}
	r.Must(rows.Err())
	r.Expect(len(out), "schema triggers")
	return out
}

var triggerInsertInto = regexp.MustCompile(`(?is)\bINSERT\s+(?:OR\s+\w+\s+)?INTO\s+([a-zA-Z_]\w*)`)

// TriggerInserts names the tables one trigger inserts into.
func TriggerInserts(sql string) []string {
	var out []string
	seen := map[string]bool{}
	for _, match := range triggerInsertInto.FindAllStringSubmatch(sql, -1) {
		table := strings.ToLower(match[1])
		if !seen[table] {
			seen[table] = true
			out = append(out, table)
		}
	}
	return out
}
