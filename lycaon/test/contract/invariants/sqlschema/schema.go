package sqlschema

import (
	"go/ast"
	"go/token"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/outbox"
)

var schema = invariants.File(invariants.SchemaPath)

var schemaRules = []invariants.Invariant{
	{Name: "session-delete-has-one-write-path", Anchors: []invariants.Anchor{invariants.Func("internal/db", "DeleteSessionTree")}, Check: sessionDeleteHasOneWritePath},
	{Name: "no-trigger-writes-the-event-outbox", Anchors: []invariants.Anchor{schema}, Check: noTriggerWritesTheOutbox},
	{Name: "event-tables-have-no-inserting-triggers", Anchors: []invariants.Anchor{schema}, Check: eventTablesHaveNoInsertingTriggers},
	{Name: "record-stamps-are-never-domain-time", Anchors: []invariants.Anchor{schema}, Exempt: stampAgedCaches, Check: recordStampsAreNeverDomainTime},
	{Name: "no-inline-ddl-in-internal-db", Anchors: []invariants.Anchor{schema}, Exempt: []invariants.Exemption{
		{Key: "internal/db/migrations/retire.go", Reason: "a registered migration step; migrations are where a released schema changes"},
	}, Check: noInlineDDLInInternalDB},
	{Name: "person-columns-are-keyed-constrained-and-indexed", Anchors: []invariants.Anchor{schema}, Check: personColumnsAreKeyedConstrainedAndIndexed},
	{Name: "person-actor-vocabulary-names-the-person", Anchors: []invariants.Anchor{schema}, Exempt: derivedPersonTables, Check: personActorVocabularyNamesThePerson},
	{Name: "every-table-declares-its-lifetime", Anchors: []invariants.Anchor{schema, invariants.Func("internal/db", "TableLifetimes")}, Check: everyTableDeclaresItsLifetime},
	{Name: "enum-columns-match-go", Anchors: []invariants.Anchor{schema}, Check: enumColumnsMatchGo},
}

// Session deletion spans rows without shared foreign keys, so one function
// writes it: db.DeleteSessionTree.
func sessionDeleteHasOneWritePath(r *invariants.Run) {
	const door = "lycaon/internal/db/session_delete.go"
	statements := []*regexp.Regexp{
		regexp.MustCompile(`(?i)DELETE\s+FROM\s+sessions\s+WHERE`),
		regexp.MustCompile(`(?i)DELETE\s+FROM\s+evidence_index\s+WHERE\s+session_id\s*=`),
	}
	var sources []struct{ rel, text string }
	for _, f := range r.GoFiles(invariants.Production, "internal", "pkg") {
		sources = append(sources, struct{ rel, text string }{"lycaon/" + f.Rel, string(f.Src)})
	}
	for _, f := range r.Tree("lycaon/internal", ".sql") {
		sources = append(sources, struct{ rel, text string }{"lycaon/internal/" + f.Rel, f.Text()})
	}
	found := false
	for _, src := range sources {
		for _, re := range statements {
			if !re.MatchString(src.text) {
				continue
			}
			if src.rel == door {
				found = true
				continue
			}
			r.Report("%s deletes sessions outside db.DeleteSessionTree: %s", src.rel, re)
		}
	}
	if !found {
		r.Report("%s holds no session delete statement", door)
	}
}

// Events for durable entities are enqueued in Go, inside the transaction
// that makes the mutation durable, never from a trigger.
func noTriggerWritesTheOutbox(r *invariants.Run) {
	for _, trigger := range outbox.Triggers(r) {
		if strings.Contains(strings.ToLower(trigger.SQL), "event_outbox") {
			r.Report("trigger %s writes event_outbox", trigger.Name)
		}
	}
}

// housekeepingTables maintain internal storage beside an announced write.
var housekeepingTables = map[string]bool{
	"content_blob_reclaim_queue":     true,
	"source_blob_reclaim_queue":      true,
	"artifact_gc_queue":              true,
	"checkpoint_resolution_ordinals": true,
	"session_source_turns":           true, // permanent attribution allocation accompanies the announced message insert
}

// A trigger on an event table may maintain search indexes and reclamation
// queues, never insert rows of its own.
func eventTablesHaveNoInsertingTriggers(r *invariants.Run) {
	events := outbox.EventTables(r)
	for _, trigger := range outbox.Triggers(r) {
		if !events[trigger.Table] {
			continue
		}
		for _, into := range outbox.TriggerInserts(trigger.SQL) {
			if !strings.Contains(into, "_fts") && !housekeepingTables[into] {
				r.Report("trigger %s on event table %s inserts into %s", trigger.Name, trigger.Table, into)
			}
		}
	}
}

// stampAgedCaches are rebuildable caches that may order or age their own
// entries by a record stamp, keyed by internal directory, and the file whose
// mtime check shows a file was written after its record.
var stampAgedCaches = []invariants.Exemption{
	{Key: "internal/blobstore", Reason: "reclaims unreferenced blobs past a grace period"},
	{Key: "internal/debugretention", Reason: "prunes debug captures by age"},
	{Key: "internal/sourceblob", Reason: "reclaims unreferenced blobs past a grace period"},
	{Key: "internal/sourcecatalog", Reason: "retires unused index generations"},
	{Key: "internal/webindex", Reason: "evicts the least recently refreshed cached pages"},
	{Key: "internal/layeredfs/working_resume.go", Reason: "a view's backing file written after its last working-log record; it orders nothing"},
}

var (
	orderByUpdatedAt = regexp.MustCompile(`(?i)\bORDER\s+BY\b[^;\x60]*?\bupdated_at\b`)
	rangeOnUpdatedAt = regexp.MustCompile(`(?i)\b([a-zA-Z0-9_.]+\.)?updated_at\s*[<>]`)
	modTimeCompare   = regexp.MustCompile(`\.ModTime\(\)\.(Before|After)\(`)
)

// A record stamp (updated_at, a file mtime) is never domain time.
func recordStampsAreNeverDomainTime(r *invariants.Run) {
	check := func(rel string, text string, patterns ...*regexp.Regexp) {
		for _, pattern := range patterns {
			if match := pattern.FindString(text); match != "" && !exemptCache(r, rel) {
				r.Report("lycaon/%s: %s", rel, match)
			}
		}
	}
	for _, f := range r.GoFiles(invariants.Production, "internal") {
		check(f.Rel, string(f.Src), orderByUpdatedAt, rangeOnUpdatedAt, modTimeCompare)
	}
	for _, f := range r.Tree("lycaon/internal", ".sql") {
		check("internal/"+f.Rel, f.Text(), orderByUpdatedAt, rangeOnUpdatedAt)
	}
}

// exemptCache reports a stamp-aged cache directory or file covering rel.
func exemptCache(r *invariants.Run, rel string) bool {
	parts := strings.Split(rel, "/")
	return len(parts) > 1 && r.Exempt(parts[0]+"/"+parts[1]) || r.Exempt(rel)
}

// The schema lives in schema.sql and migrations, never in internal/db Go.
func noInlineDDLInInternalDB(r *invariants.Run) {
	for _, f := range r.GoFiles(func(f *invariants.GoFile) bool {
		base := f.Rel[strings.LastIndexByte(f.Rel, '/')+1:]
		return !f.IsTest && !strings.HasSuffix(base, ".sql.go") && base != "models.go"
	}, "internal/db") {
		if r.Exempt(f.Rel) {
			continue
		}
		ast.Inspect(f.AST, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for _, ddl := range []string{"ALTER TABLE", "CREATE TABLE", "CREATE INDEX", "DROP COLUMN"} {
				if strings.Contains(lit.Value, ddl) {
					r.ReportAt(lit.Pos(), "contains %q; edit schema.sql instead", ddl)
					break
				}
			}
			return true
		})
	}
}

// enumColumns pair a CHECK-constrained column with the Go values it stores.
var enumColumns = []struct {
	table, column string
	values        []string
}{
	{"people", "role", wirespec.GoEnumValues(api.PersonRoleOwner)},
	{"sessions", "posture", wirespec.GoEnumValues(api.SessionPostureSpec, api.SessionPostureVet, api.SessionPostureOrchestrate, api.SessionPostureBuild)},
	{"sessions", "status", wirespec.GoEnumValues(api.SessionStatusPreparing, api.SessionStatusIdle, api.SessionStatusBusy, api.SessionStatusError)},
	{"messages", "role", wirespec.GoEnumValues(api.MessageRoleUser, api.MessageRoleAssistant, api.MessageRoleTool, api.MessageRoleSystem)},
	{"worker_jobs", "status", wirespec.GoEnumValues(api.AllWorkerStatuses()...)},
	{"worker_jobs", "spawn_reason", wirespec.GoEnumValues(api.SpawnReasonInitial, api.SpawnReasonRetry, api.SpawnReasonHumanRequest, api.SpawnReasonCloseout)},
	{"worker_jobs", "execution_target", wirespec.GoEnumValues(api.ExecutionTargetLocal, api.ExecutionTargetRunner)},
	{"code_scans", "status", wirespec.GoEnumValues(api.CodeScanStatusPending, api.CodeScanStatusRunning, api.CodeScanStatusComplete, api.CodeScanStatusFailed, api.CodeScanStatusTimedOut, api.CodeScanStatusCanceled, api.CodeScanStatusSuperseded)},
	// The ledger stores only recorded states; not_observed, unverified and
	// ignored are derived from absence facts at read time.
	{"scan_finding_ledger", "state", wirespec.GoEnumValues(api.FindingLedgerOpen, api.FindingLedgerReopened, api.FindingLedgerFixed)},
}

// Each enum column's CHECK lists exactly the Go values it stores.
func enumColumnsMatchGo(r *invariants.Run) {
	for _, c := range enumColumns {
		values, ok := checkValues(tableSQL(r, c.table), c.column)
		if !ok {
			r.Report("%s.%s has no CHECK listing its values", c.table, c.column)
			continue
		}
		r.Diff(c.table+"."+c.column, c.values, values)
	}
}

// checkValues returns the values a CHECK (column IN (…)) admits.
func checkValues(sql, column string) ([]string, bool) {
	for _, clause := range checkClauses(sql) {
		m := regexp.MustCompile(`(?is)\b` + regexp.QuoteMeta(column) + `\s+IN\s*\(([^)]*)\)`).FindStringSubmatch(clause)
		if m == nil {
			continue
		}
		var out []string
		for _, raw := range strings.Split(m[1], ",") {
			if value := strings.Trim(strings.TrimSpace(raw), "'"); value != "" {
				out = append(out, value)
			}
		}
		return out, true
	}
	return nil, false
}

// Every table declares its lifetime: owned through a cascading foreign key
// to its owner, or a reasoned class with no owner.
func everyTableDeclaresItsLifetime(r *invariants.Run) {
	parents := cascadeParents(r)
	lifetimes := db.TableLifetimes()
	for _, table := range sortedKeys(parents) {
		lifetime, ok := lifetimes[table]
		if !ok {
			r.Report("%s has no entry in db.TableLifetimes", table)
			continue
		}
		switch lifetime.Class {
		case db.LifetimeCascadeOwned:
			if !parents[table][lifetime.Owner] {
				r.Report("%s has no ON DELETE CASCADE foreign key to its owner %s", table, lifetime.Owner)
			}
		case db.LifetimeForever, db.LifetimeReceiptWithRetention, db.LifetimeProjection, db.LifetimeCache:
			if lifetime.Reason == "" || lifetime.Owner != "" {
				r.Report("%s: %s needs a reason and no owner", table, lifetime.Class)
			}
		default:
			r.Report("%s: unknown lifetime class %s", table, lifetime.Class)
		}
	}
	for _, table := range sortedKeys(lifetimes) {
		if _, ok := parents[table]; !ok {
			r.Report("%s is in db.TableLifetimes but not in the schema", table)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
