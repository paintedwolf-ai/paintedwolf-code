package contract

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// outboxTableFloor covers event tables not discovered through direct writes.
var outboxTableFloor = []string{
	"worker_jobs",
	"workflow_runs",
	"delegations",
	"delegation_legs",
	"code_scans",
}

// outboxSilentMutators names functions that write an event-carrying table
// outside any announcing transaction, keyed by file:function, each with the
// reason the write needs no outbox event of its own.
var outboxSilentMutators = map[string]string{
	// Path reservations.
	"lycaon/internal/call/sql_store.go:InsertReservation":             "records one path reservation; reservations are coordination state that worker promotion releases in its own transaction",
	"lycaon/internal/call/sql_store.go:DeleteReservation":             "releases one path reservation",
	"lycaon/internal/call/sql_store.go:DeleteAllReservationsForAgent": "clears an agent's path reservations",

	// Project rows carry storage and allocation bookkeeping beside the wire.
	"lycaon/internal/contentblob/density.go:densifyProject":         "marks a densified project's storage tier cold; the tier is storage bookkeeping, not on the project wire",
	"lycaon/internal/project/sql_registry.go:SetCover":              "moves the project cover reference; session.Manager publishes the project update after visual.DesignateCover",
	"lycaon/internal/sourceledger/gittrack.go:commitGitTransitions": "records observed git ref transitions; its projects write only allocates their source-history ordinals",
	"lycaon/internal/sourceledger/commandwindow.go:insertWindow":    "opens a command window; its projects write only allocates the window's source-history ordinal, which is off the project wire",

	// The session plan is published through progress write observers.
	"lycaon/internal/progress/sql.go:Set":       "writes the session plan; progress write observers publish it through events.Publisher.PublishProgress",
	"lycaon/internal/progress/sql.go:EnsureRun": "reseeds the session plan for a new workflow run; session.Manager.maybeBootstrapProgress notifies the progress write observers that publish it",
	"lycaon/internal/progress/sql.go:BindRun":   "binds the session plan to its workflow run; the plan wire carries only the content, which the binding leaves unchanged",

	// Security scan bookkeeping; the scans announce through emitScanTx.
	"lycaon/internal/scan/authority_store.go:EnsureAssessment":            "records the assessment a scan joins; the scans that join it announce through emitScanTx, which refreshes the assessment rollup",
	"lycaon/internal/scan/lease.go:RenewClaim":                            "extends a scan claim's lease deadline",
	"lycaon/internal/scan/ledger_ignore.go:applyIgnores":                  "rewrites the ledger's cached ignore verdicts from the ignore file; reads re-apply when the file's digest changes",
	"lycaon/internal/scan/ledger_ignore.go:InvalidateIgnoreDigest":        "forces the next ledger read to re-apply the ignore file",
	"lycaon/internal/scan/store_execution.go:MarkDelta":                   "records a running scan's delta against its base; the scan's terminal transition announces it through emitScanTx",
	"lycaon/internal/scan/store_contexts.go:MarkWorkflowTerminalNotified": "acknowledges that a terminal scan reached its workflow run's gate; the acknowledgement is a delivery cursor off the scan wire, and the run announces its own gate refresh",

	// Prompt submission receipts reach clients only as the reply to a prompt
	// admission or its replay; the queue draft and the run announce the rest.
	"lycaon/internal/session/store/sql_prompt_submission.go:PutPromptSubmission":                        "admits a queue receipt; session.Manager publishes the queue snapshot through events.Publisher.PublishQueue",
	"lycaon/internal/session/store/sql_prompt_submission.go:ClaimPromptSubmission":                      "claims one admitted receipt for execution; the run announces through its session and message events",
	"lycaon/internal/session/store/sql_prompt_submission.go:ClaimPromptSubmissions":                     "claims queued receipts for one dispatch; the queue snapshot is published through PublishQueue",
	"lycaon/internal/session/store/sql_prompt_submission.go:FinishPromptSubmission":                     "finishes a claimed submission",
	"lycaon/internal/session/store/sql_prompt_submission.go:CancelQueuedPromptSubmissions":              "cancels queued receipts; the queue snapshot is published through PublishQueue",
	"lycaon/internal/session/store/sql_prompt_submission.go:UpdateQueuedPromptSubmissionInputs":         "edits queued receipt inputs; the queue snapshot is published through PublishQueue",
	"lycaon/internal/session/store/sql_prompt_submission.go:InterruptPromptSubmissionsBySession":        "interrupts a stopped session's receipts; the stop path publishes the queue snapshot",
	"lycaon/internal/session/store/sql_prompt_submission.go:InterruptRunningPromptSubmissionsBySession": "interrupts a stopped session's running receipt; the stop path publishes the queue snapshot",
	"lycaon/internal/session/store/sql_prompt_submission.go:RecoverPromptSubmissions":                   "requeues interrupted receipts at boot, before any subscriber; recovered submissions announce as they dispatch",
	"lycaon/internal/session/store/turn_sql.go:RecoverTurns":                                            "completes running receipts whose turns finished before host exit, at boot before any subscriber; the idle transitions after it announce the sessions",
	"lycaon/internal/session/store/turn_sql.go:RecoverTurnsForSession":                                  "completes a panicked session's running receipts whose turns finished; the session's idle transition after it announces the recovery",

	// Session and transcript rows written beside an announced transition.
	"lycaon/internal/session/store/secret_screen_generation.go:StampMessageScreenGeneration": "stamps the evidence revision a row was screened at; the content rewrite beside it goes through UpdateMessage, which announces",
	"lycaon/internal/session/store/sql_transcript_write.go:PatchLiveProjection":              "patches the streaming draft behind stream.State.persistLiveProjection; the stream hub delivers the live projection",
	"lycaon/internal/session/store/workspace_root_reassign.go:ReassignSessionsWorkspaceRoot": "repoints sessions at a new workspace root",
	"lycaon/internal/session/store/rewind.go:PrepareRewind":                                  "opens a rewind operation row",
	"lycaon/internal/session/store/rewind.go:SetRewindPhase":                                 "advances a rewind operation's phase",
	"lycaon/internal/session/store/rewind.go:DeleteRewindOperation":                          "removes a committed rewind's internal recovery receipt after retention",
	"lycaon/internal/session/decisions/sql.go:Put":                                           "stores a decision surfaced by worker completion",
	"lycaon/internal/session/decisions/sql.go:Clear":                                         "removes a decision after the worker resumes",

	// Worker and workflow internal receipts and leases.
	"lycaon/internal/worker/lease.go:RenewClaim":                                       "extends a worker job's lease deadline",
	"lycaon/internal/worker/sql_merge_lease.go:RenewMergeApply":                        "extends a worker job's merge-apply lease deadline",
	"lycaon/internal/worker/sql_query.go:MarkOutcomeDelivered":                         "records that a worker outcome reached its parent; the parent's transcript append announces the delivery",
	"lycaon/internal/worker/sql_queue_cancellation.go:RequestCancellation":             "fences a job's claims and outcomes before its runtime stops; the job announces when cancellation settles",
	"lycaon/internal/workflow/persistence/verdicts.go:PrepareVerdictOperation":         "prepares an internal replay/recovery receipt; the workflow transition announces atomically when the verdict commits",
	"lycaon/internal/workflow/persistence/verdicts.go:MarkVerdictEvidenceApplied":      "marks the internal recovery receipt's evidence phase; no run projection changes until the verdict commits",
	"lycaon/internal/workflow/persistence/verdicts.go:ResolveVerdictOperationDiverged": "terminally resolves an internal recovery receipt without changing the run; diagnostic recovery state is not a workflow wire transition",
	"lycaon/internal/workflow/persistence/verdicts.go:RebaseVerdictOperation":          "moves a prepared verdict operation, an internal recovery receipt, onto a newer phase revision; the verdict announces when it commits",
}

func TestNoSQLTriggerWritesTheEventOutbox(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schema := contractcheck.ReadRepoFile(t, root, filepath.Join("lycaon", "internal", "db", "schema.sql"))
	var offenders []string
	for _, trigger := range sqlTriggerBodies(schema) {
		if strings.Contains(strings.ToLower(trigger.body), "event_outbox") {
			offenders = append(offenders, trigger.name)
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("Anti-drift: SQL triggers write event_outbox: %s\n"+
			"Events for durable entities are enqueued in Go with EnqueueTx, inside the transaction that "+
			"makes the mutation durable — see internal/*/sql_events.go.", strings.Join(offenders, ", "))
	}
}

// These tables track storage reclamation and internal allocation.
var outboxHousekeepingTables = map[string]bool{
	"content_blob_reclaim_queue":     true,
	"source_blob_reclaim_queue":      true,
	"artifact_gc_queue":              true,
	"checkpoint_resolution_ordinals": true,
	"session_source_turns":           true, // permanent attribution allocation accompanies the announced message insert
}

func TestEventTablesHaveNoEventTriggers(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scan := scanOutboxStores(t, root)
	covered := scan.eventTables
	schema := contractcheck.ReadRepoFile(t, root, filepath.Join("lycaon", "internal", "db", "schema.sql"))
	var offenders []string
	for _, trigger := range sqlTriggerBodies(schema) {
		if !covered[trigger.table] {
			continue
		}
		for _, into := range triggerInserts(trigger.body) {
			// Search indexes and reclamation queues maintain internal storage.
			if strings.Contains(into, "_fts") {
				continue
			}
			if outboxHousekeepingTables[into] {
				continue
			}
			offenders = append(offenders, trigger.name+" on "+trigger.table+" inserts "+into)
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("Anti-drift: inserting trigger on an outbox-covered table: %s", strings.Join(offenders, ", "))
	}
}

// TestOutboxMutatorsAnnounceTheirTransition checks each discovered mutator.
func TestOutboxMutatorsAnnounceTheirTransition(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scan := scanOutboxStores(t, root)

	unused := map[string]bool{}
	for key := range outboxSilentMutators {
		unused[key] = true
	}
	var problems []string
	var offenders []string
	for key, tables := range scan.silentMutators() {
		if unused[key] {
			delete(unused, key)
			continue
		}
		offenders = append(offenders, fmt.Sprintf("%s (%s)", key, scan.describeTables(tables)))
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		problems = append(problems, fmt.Sprintf("Anti-drift: these mutate an event-carrying table and announce nothing:\n  %s\n"+
			"Enqueue the transition with EnqueueTx inside the transaction that makes it durable, or pin "+
			"the function with the write it performs.", strings.Join(offenders, "\n  ")))
	}
	if len(unused) > 0 {
		stale := make([]string, 0, len(unused))
		for key := range unused {
			stale = append(stale, key)
		}
		sort.Strings(stale)
		problems = append(problems, fmt.Sprintf("pinned silent mutators that no longer mutate an event-carrying table "+
			"(delete the line, or they moved):\n  %s", strings.Join(stale, "\n  ")))
	}
	if len(problems) > 0 {
		t.Fatal(strings.Join(problems, "\n\n"))
	}
}

// TestOutboxEnqueuesTakeAMutationTransaction requires a caller-held transaction.
func TestOutboxEnqueuesTakeAMutationTransaction(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scan := scanOutboxStores(t, root)
	if len(scan.enqueueSites) == 0 {
		t.Fatal("no EnqueueTx call sites found — the scan is broken, not the code")
	}
	var offenders []string
	for _, site := range scan.enqueueSites {
		if !site.heldTx {
			offenders = append(offenders, site.where+": "+site.arg)
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("Anti-drift: enqueue outside the mutation's transaction:\n  %s", strings.Join(offenders, "\n  "))
	}
}
