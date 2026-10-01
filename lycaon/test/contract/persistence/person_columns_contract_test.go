package contract

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// actorColumns hold who acted on a row. A table whose declared vocabulary for
// one of them names a person must record which person.
var actorColumns = []string{"origin", "actor_kind", "role", "resolved_by", "revoked_by", "created_by"}

// personActorValues are actor vocabulary that means a person acted.
var personActorValues = []string{"user", "human", "person", "user_stop", "restore"}

// derivedPersonTables name their person through a linked row instead of a column.
var derivedPersonTables = map[string]string{
	"turns": "a user turn's people are its prompt_submissions.submitted_by, linked by turn_submissions",
}

type personColumn struct {
	table, column string
	notNull       bool
}

func TestPersonColumnsAreKeyedConstrainedAndIndexed(t *testing.T) {
	t.Parallel()
	store := openPersonSchemaStore(t)
	tables := schemaTables(t, store)
	var violations []string
	for _, col := range peopleReferences(t, store, tables) {
		sql := tableSQL(t, store, col.table)
		if !col.notNull && !checkMentions(sql, col.column) {
			violations = append(violations, col.table+"."+col.column+
				": a nullable person column needs a CHECK that says when it is absent")
		}
		if !leadsAnIndex(t, store, col.table, col.column) {
			violations = append(violations, col.table+"."+col.column+": no index leads with this person column")
		}
	}
	for _, table := range tables {
		for _, column := range tableColumns(t, store, table) {
			if strings.HasSuffix(column, "person_id") && !referencesPeople(t, store, table, column) {
				violations = append(violations, table+"."+column+": a person column must reference people(id)")
			}
		}
	}
	sort.Strings(violations)
	contractcheck.FailViolations(t, "person column violations", violations)
}

func TestPersonActorVocabularyNamesThePerson(t *testing.T) {
	t.Parallel()
	store := openPersonSchemaStore(t)
	tables := schemaTables(t, store)
	withPeople := map[string]bool{}
	for _, col := range peopleReferences(t, store, tables) {
		withPeople[col.table] = true
	}
	unused := map[string]bool{}
	for table := range derivedPersonTables {
		unused[table] = true
	}
	var violations []string
	for _, table := range tables {
		if table == "people" {
			continue
		}
		column, value, ok := personActor(tableSQL(t, store, table))
		if !ok {
			continue
		}
		if _, derived := derivedPersonTables[table]; derived {
			delete(unused, table)
			continue
		}
		if !withPeople[table] {
			violations = append(violations, table+"."+column+" = '"+value+"' names a person but the table has no people(id) column")
		}
	}
	sort.Strings(violations)
	contractcheck.FailViolations(t, "rows a person acted on without naming the person", violations)
	var stale []string
	for table := range unused {
		stale = append(stale, table)
	}
	sort.Strings(stale)
	contractcheck.FailViolations(t, "stale derivedPersonTables entries", stale)
}

func openPersonSchemaStore(t *testing.T) *db.Store {
	t.Helper()
	store := testdbfixture.Open(t, "store.db")
	return store
}

func schemaTables(t *testing.T, store *db.Store) []string {
	t.Helper()
	rows, err := store.QueryContext(t.Context(), `
		SELECT name FROM pragma_table_list
		WHERE schema = 'main' AND type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	contractcheck.FailErr(t, "list tables", err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		contractcheck.FailErr(t, "scan table", rows.Scan(&name))
		out = append(out, name)
	}
	contractcheck.FailErr(t, "iterate tables", rows.Err())
	if len(out) == 0 {
		t.Fatal("opened store has no tables")
	}
	return out
}

func peopleReferences(t *testing.T, store *db.Store, tables []string) []personColumn {
	t.Helper()
	var out []personColumn
	for _, table := range tables {
		rows, err := store.QueryContext(t.Context(), `
			SELECT fk."from", ti."notnull"
			FROM pragma_foreign_key_list(?) AS fk
			JOIN pragma_table_info(?) AS ti ON ti.name = fk."from"
			WHERE fk."table" = 'people'`, table, table)
		contractcheck.FailErr(t, "list people references of "+table, err)
		for rows.Next() {
			var col personColumn
			col.table = table
			contractcheck.FailErr(t, "scan people reference", rows.Scan(&col.column, &col.notNull))
			out = append(out, col)
		}
		contractcheck.FailErr(t, "iterate people references", rows.Err())
		_ = rows.Close()
	}
	if len(out) == 0 {
		t.Fatal("no columns reference people(id)")
	}
	return out
}

func tableColumns(t *testing.T, store *db.Store, table string) []string {
	t.Helper()
	rows, err := store.QueryContext(t.Context(), `SELECT name FROM pragma_table_info(?)`, table)
	contractcheck.FailErr(t, "list columns of "+table, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		contractcheck.FailErr(t, "scan column", rows.Scan(&name))
		out = append(out, name)
	}
	contractcheck.FailErr(t, "iterate columns", rows.Err())
	return out
}

func referencesPeople(t *testing.T, store *db.Store, table, column string) bool {
	t.Helper()
	var n int
	contractcheck.FailErr(t, "check people reference", store.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM pragma_foreign_key_list(?)
		WHERE "from" = ? AND "table" = 'people'`, table, column).Scan(&n))
	return n > 0
}

func leadsAnIndex(t *testing.T, store *db.Store, table, column string) bool {
	t.Helper()
	var n int
	contractcheck.FailErr(t, "check index coverage", store.QueryRowContext(t.Context(), `
		SELECT COUNT(*)
		FROM pragma_index_list(?) AS il
		JOIN pragma_index_info(il.name) AS ii ON ii.seqno = 0
		WHERE ii.name = ?`, table, column).Scan(&n))
	return n > 0
}

func tableSQL(t *testing.T, store *db.Store, table string) string {
	t.Helper()
	var sql string
	contractcheck.FailErr(t, "read "+table+" definition", store.QueryRowContext(t.Context(),
		`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&sql))
	return sql
}

// checkClauses returns the body of every CHECK constraint in a table definition.
func checkClauses(sql string) []string {
	var out []string
	upper := strings.ToUpper(sql)
	for i := 0; ; {
		at := strings.Index(upper[i:], "CHECK")
		if at < 0 {
			return out
		}
		start := strings.IndexByte(sql[i+at:], '(')
		if start < 0 {
			return out
		}
		start += i + at
		depth := 0
		end := start
		for ; end < len(sql); end++ {
			switch sql[end] {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		out = append(out, sql[start:min(end+1, len(sql))])
		i = end + 1
		if i >= len(sql) {
			return out
		}
	}
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
