package sqlschema

import (
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// actorColumns hold who acted on a row.
var actorColumns = []string{"origin", "actor_kind", "role", "resolved_by", "revoked_by", "created_by"}

// personActorValues are actor vocabulary that means a person acted.
var personActorValues = []string{"user", "human", "person", "user_stop", "restore"}

// derivedPersonTables name their person through a linked row.
var derivedPersonTables = []invariants.Exemption{
	{Key: "turns", Reason: "a user turn's people are its prompt_submissions.submitted_by, linked by turn_submissions"},
}

type personColumn struct {
	table, column string
	notNull       bool
}

// A people(id) column is constrained when nullable and leads an index, and
// every *person_id column references people(id).
func personColumnsAreKeyedConstrainedAndIndexed(r *invariants.Run) {
	for _, col := range peopleReferences(r) {
		if !col.notNull && !checkMentions(tableSQL(r, col.table), col.column) {
			r.Report("%s.%s: a nullable person column needs a CHECK that says when it is absent", col.table, col.column)
		}
		if count(r, `SELECT COUNT(*) FROM pragma_index_list(?) AS il JOIN pragma_index_info(il.name) AS ii ON ii.seqno = 0 WHERE ii.name = ?`, col.table, col.column) == 0 {
			r.Report("%s.%s: no index leads with this person column", col.table, col.column)
		}
	}
	for _, table := range tables(r) {
		for _, column := range columnValues(r, `SELECT name FROM pragma_table_info(?)`, table) {
			if strings.HasSuffix(column, "person_id") && count(r, `SELECT COUNT(*) FROM pragma_foreign_key_list(?) WHERE "from" = ? AND "table" = 'people'`, table, column) == 0 {
				r.Report("%s.%s: a person column must reference people(id)", table, column)
			}
		}
	}
}

// A row whose actor vocabulary names a person records which person.
func personActorVocabularyNamesThePerson(r *invariants.Run) {
	withPeople := map[string]bool{}
	for _, col := range peopleReferences(r) {
		withPeople[col.table] = true
	}
	for _, table := range tables(r) {
		if table == "people" {
			continue
		}
		column, value, ok := personActor(tableSQL(r, table))
		if ok && !withPeople[table] && !r.Exempt(table) {
			r.Report("%s.%s = '%s' names a person but the table has no people(id) column", table, column, value)
		}
	}
}

func peopleReferences(r *invariants.Run) []personColumn {
	var out []personColumn
	for _, table := range tables(r) {
		rows, err := r.Schema().Query(`
			SELECT fk."from", ti."notnull"
			FROM pragma_foreign_key_list(?) AS fk
			JOIN pragma_table_info(?) AS ti ON ti.name = fk."from"
			WHERE fk."table" = 'people'`, table, table)
		r.Must(err)
		for rows.Next() {
			col := personColumn{table: table}
			r.Must(rows.Scan(&col.column, &col.notNull))
			out = append(out, col)
		}
		r.Must(rows.Err())
		_ = rows.Close()
	}
	r.Expect(len(out), "columns referencing people(id)")
	return out
}

// tables lists the ordinary tables of the live schema.
func tables(r *invariants.Run) []string {
	out := columnValues(r, `SELECT name FROM pragma_table_list WHERE schema = 'main' AND type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	r.Expect(len(out), "schema tables")
	return out
}

// cascadeParents maps every table to the tables its ON DELETE CASCADE
// foreign keys reference.
func cascadeParents(r *invariants.Run) map[string]map[string]bool {
	rows, err := r.Schema().Query(`
		SELECT tl.name, COALESCE(fk."table", '')
		FROM pragma_table_list AS tl
		LEFT JOIN pragma_foreign_key_list(tl.name) AS fk ON fk.on_delete = 'CASCADE'
		WHERE tl.schema = 'main' AND tl.type = 'table' AND tl.name NOT LIKE 'sqlite_%'`)
	r.Must(err)
	defer func() { _ = rows.Close() }()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var table, parent string
		r.Must(rows.Scan(&table, &parent))
		if out[table] == nil {
			out[table] = map[string]bool{}
		}
		if parent != "" {
			out[table][parent] = true
		}
	}
	r.Must(rows.Err())
	r.Expect(len(out), "schema tables")
	return out
}

func tableSQL(r *invariants.Run, table string) string {
	var sql string
	if err := r.Schema().QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&sql); err != nil {
		r.Fatalf("read the definition of %s: %v", table, err)
	}
	return sql
}

func columnValues(r *invariants.Run, query string, args ...any) []string {
	rows, err := r.Schema().Query(query, args...)
	r.Must(err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		r.Must(rows.Scan(&s))
		out = append(out, s)
	}
	r.Must(rows.Err())
	return out
}

func count(r *invariants.Run, query string, args ...any) int {
	var n int
	r.Must(r.Schema().QueryRow(query, args...).Scan(&n))
	return n
}

// checkClauses returns the body of every CHECK constraint in a definition.
func checkClauses(sql string) []string {
	var out []string
	upper := strings.ToUpper(sql)
	for i := 0; i < len(sql); {
		at := strings.Index(upper[i:], "CHECK")
		if at < 0 {
			break
		}
		start := strings.IndexByte(sql[i+at:], '(')
		if start < 0 {
			break
		}
		start += i + at
		depth, end := 0, start
		for ; end < len(sql); end++ {
			if sql[end] == '(' {
				depth++
			} else if sql[end] == ')' {
				depth--
			}
			if depth == 0 {
				break
			}
		}
		out = append(out, sql[start:min(end+1, len(sql))])
		i = end + 1
	}
	return out
}

func checkMentions(sql, column string) bool {
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(column) + `\b`)
	for _, clause := range checkClauses(sql) {
		if word.MatchString(clause) {
			return true
		}
	}
	return false
}

// personActor finds an actor column whose declared vocabulary names a person.
func personActor(sql string) (column, value string, ok bool) {
	for _, clause := range checkClauses(sql) {
		for _, col := range actorColumns {
			re := regexp.MustCompile(`\b` + col + `\b\s*(?:=\s*'([^']*)'|IN\s*\(([^)]*)\))`)
			for _, m := range re.FindAllStringSubmatch(clause, -1) {
				for _, v := range personActorValues {
					if m[1] == v || strings.Contains(m[2], "'"+v+"'") {
						return col, v, true
					}
				}
			}
		}
	}
	return "", "", false
}
