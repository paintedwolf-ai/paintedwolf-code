package outbox

import (
	"slices"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// Family is the outbox-closure rule table: every durable transition that
// carries an event is enqueued inside the transaction that makes it durable.
var Family = laws.Family{Name: "outbox-closure", Rules: []laws.Rule{
	{Name: "mutators-announce-their-transition", Anchors: []laws.Anchor{outboxDoor}, Exempt: silentMutators, Check: mutatorsAnnounceTheirTransition},
	{Name: "enqueues-take-a-mutation-transaction", Anchors: []laws.Anchor{outboxDoor}, Check: enqueuesTakeAMutationTransaction},
	{Name: "outbox-bearing-stores-are-wired", Anchors: []laws.Anchor{laws.Package("internal/app")}, Check: everyOutboxBearingStoreIsWired},
}}

var outboxDoor = laws.Decl("internal/eventoutbox", "Outbox.EnqueueTx")

// silentMutators write an event-carrying table outside any announcing
// transaction, each with the reason the write needs no event of its own.
var silentMutators = []laws.Exemption{
	// Worker rounds settle inside job completion and cancellation, which
	// announce the job; no event payload carries a round.
	{Key: "internal/workerrecords/rounds.go:AdmitRound", Reason: "records the round an admitted job runs; the job announces its own admission and no event carries the round"},
	{Key: "internal/workerrecords/rounds.go:StartRound", Reason: "records the round an instruction starts and the child session's agent; the job announces its admission, and neither the round nor the agent is on an event payload"},
	// Workers bind inside the job's child-session transaction; the roster reads
	// worker records on each request and no event payload carries one.
	{Key: "internal/workerrecords/workers.go:CreateWorker", Reason: "creates a worker for the task that starts it; the job announces its admission"},
	{Key: "internal/workerrecords/workers.go:UpdatePlanned", Reason: "reconciles an unstarted planned worker from a blueprint; the roster reads worker records on each request"},
	{Key: "internal/workerrecords/workers.go:SetBrief", Reason: "changes a worker's brief, which no event payload carries"},
	{Key: "internal/workerrecords/workers.go:Close", Reason: "closes a worker; the roster drops it on the next request"},
	{Key: "internal/workerrecords/workers.go:Discard", Reason: "rolls back a roundless worker and its create receipt when task admission fails before a job starts; no event announced the worker"},
	{Key: "internal/workerrecords/messages.go:AppendMessage", Reason: "stores a message and advances its worker's sequence; recipients read their page on their next request"},
	{Key: "internal/workerrecords/dispositions.go:AccountJobs", Reason: "marks results accounted for a job list; the roster reads dispositions on each request and no event payload carries one"},
	{Key: "internal/workerrecords/dispositions.go:AccountResult", Reason: "marks one result accounted; the roster reads dispositions on each request and no event payload carries one"},
	{Key: "internal/workerrecords/dispositions.go:SetChange", Reason: "records a result's change state; the roster reads dispositions on each request and no event payload carries one"},

	// Path reservations.
	{Key: "internal/call/sql_store.go:InsertReservation", Reason: "records one path reservation; reservations are coordination state that worker promotion releases in its own transaction"},
	{Key: "internal/call/sql_store.go:DeleteReservation", Reason: "releases one path reservation"},
	{Key: "internal/call/sql_store.go:DeleteAllReservationsForAgent", Reason: "clears an agent's path reservations"},

	// Project rows carry storage and allocation bookkeeping beside the wire.
	{Key: "internal/contentblob/density.go:densifyProject", Reason: "marks a densified project's storage tier cold; the tier is storage bookkeeping, not on the project wire"},
	{Key: "internal/project/sql_registry.go:SetCover", Reason: "moves the project cover reference; session.Manager publishes the project update after visual.DesignateCover"},
	{Key: "internal/sourceledger/gittrack.go:commitGitTransitions", Reason: "records observed git ref transitions; its projects write only allocates their source-history ordinals"},
	{Key: "internal/sourceledger/commandwindow.go:insertWindow", Reason: "opens a command window; its projects write only allocates the window's source-history ordinal, which is off the project wire"},

	// The session plan is published through progress write observers.

	{Key: "internal/progress/snapshot_store.go:Update", Reason: "commits one revision-aware progress update; ApplyAuthorUpdate notifies the registered progress write observers after the transaction"},
	{Key: "internal/progress/sql.go:Set", Reason: "writes the session plan; progress write observers publish it through events.Publisher.PublishProgress"},
	{Key: "internal/progress/sql.go:EnsureRun", Reason: "reseeds the session plan for a new workflow run; session.Manager.maybeBootstrapProgress notifies the progress write observers that publish it"},
	{Key: "internal/progress/sql.go:BindRun", Reason: "binds the session plan to its workflow run; the plan wire carries only the content, which the binding leaves unchanged"},

	// Security scan bookkeeping; the scans announce through emitScanTx.
	{Key: "internal/scan/authority_store.go:EnsureAssessment", Reason: "records the assessment a scan joins; the scans that join it announce through emitScanTx, which refreshes the assessment rollup"},
	{Key: "internal/scan/lease.go:RenewClaim", Reason: "extends a scan claim's lease deadline"},
	{Key: "internal/scan/ledger_ignore.go:applyIgnores", Reason: "rewrites the ledger's cached ignore verdicts from the ignore file; reads re-apply when the file's digest changes"},
	{Key: "internal/scan/ledger_ignore.go:InvalidateIgnoreDigest", Reason: "forces the next ledger read to re-apply the ignore file"},
	{Key: "internal/scan/store_execution.go:MarkDelta", Reason: "records a running scan's delta against its base; the scan's terminal transition announces it through emitScanTx"},
	{Key: "internal/scan/store_contexts.go:MarkWorkflowTerminalNotified", Reason: "acknowledges that a terminal scan reached its workflow run's gate; the acknowledgement is a delivery cursor off the scan wire, and the run announces its own gate refresh"},

	// Prompt submission receipts reach clients only as the reply to a prompt
	// admission or its replay; the queue draft and the run announce the rest.
	{Key: "internal/session/store/sql_prompt_submission.go:putPromptSubmissionSQL", Reason: "admits a queue receipt; session.Manager publishes the queue snapshot through events.Publisher.PublishQueue"},
	{Key: "internal/session/store/sql_prompt_submission.go:claimPromptSubmissionSQL", Reason: "claims one admitted receipt for execution; the run announces through its session and message events"},
	{Key: "internal/session/store/sql_prompt_submission.go:claimPromptSubmissionsSQL", Reason: "claims queued receipts for one dispatch; the queue snapshot is published through PublishQueue"},
	{Key: "internal/session/store/sql_prompt_submission.go:FinishPromptSubmission", Reason: "finishes a claimed submission"},
	{Key: "internal/session/store/sql_prompt_submission.go:CancelQueuedPromptSubmissions", Reason: "cancels queued receipts; the queue snapshot is published through PublishQueue"},
	{Key: "internal/session/store/sql_prompt_submission.go:UpdateQueuedPromptSubmissionInputs", Reason: "edits queued receipt inputs; the queue snapshot is published through PublishQueue"},
	{Key: "internal/session/store/sql_prompt_attachment.go:InterruptPromptSubmissionsBySession", Reason: "interrupts a stopped session's receipts; the stop path publishes the queue snapshot"},
	{Key: "internal/session/store/sql_prompt_attachment.go:InterruptRunningPromptSubmissionsBySession", Reason: "interrupts a stopped session's running receipt; the stop path publishes the queue snapshot"},
	{Key: "internal/session/store/sql_prompt_attachment.go:recoverPromptSubmissionsSQL", Reason: "requeues interrupted receipts at boot, before any subscriber; recovered submissions announce as they dispatch"},
	{Key: "internal/session/store/turn_sql_recovery.go:recoverTurnsSQL", Reason: "completes running receipts whose turns finished before host exit, at boot before any subscriber; the idle transitions after it announce the sessions"},
	{Key: "internal/session/store/turn_sql_recovery.go:recoverTurnsForSessionSQL", Reason: "completes a panicked session's running receipts whose turns finished; the session's idle transition after it announces the recovery"},

	// Session and transcript rows written beside an announced transition.
	{Key: "internal/session/store/secret_screen_generation.go:StampMessageScreenGeneration", Reason: "stamps the evidence revision a row was screened at; the content rewrite beside it goes through UpdateMessage, which announces"},
	{Key: "internal/session/store/append.go:PatchLiveProjection", Reason: "patches the streaming draft behind stream.State.persistLiveProjection; the stream hub delivers the live projection"},
	{Key: "internal/session/store/workspace_root_reassign.go:ReassignSessionsWorkspaceRoot", Reason: "repoints sessions at a new workspace root"},
	{Key: "internal/session/store/rewind.go:PrepareRewind", Reason: "opens a rewind operation row"},
	{Key: "internal/session/store/rewind.go:SetRewindPhase", Reason: "advances a rewind operation's phase"},
	{Key: "internal/session/store/rewind.go:DeleteRewindOperation", Reason: "removes a committed rewind's internal recovery receipt after retention"},
	{Key: "internal/session/decisions/sql.go:Put", Reason: "stores a decision surfaced by worker completion"},
	{Key: "internal/session/decisions/sql.go:Clear", Reason: "removes a decision after the worker resumes"},

	// Worker and workflow internal receipts and leases.
	{Key: "internal/worker/lease.go:RenewClaim", Reason: "extends a worker job's lease deadline"},
	{Key: "internal/worker/sql_merge_lease.go:RenewMergeApply", Reason: "extends a worker job's merge-apply lease deadline"},
	{Key: "internal/worker/sql_query.go:MarkOutcomeDelivered", Reason: "records that a worker outcome reached its parent; the parent's transcript append announces the delivery"},
	{Key: "internal/worker/sql_queue_enqueue.go:RequestCancellation", Reason: "fences a job's claims and outcomes before its runtime stops; the job announces when cancellation settles"},
	{Key: "internal/workflow/persistence/verdicts.go:PrepareVerdictOperation", Reason: "prepares an internal replay/recovery receipt; the workflow transition announces atomically when the verdict commits"},
	{Key: "internal/workflow/persistence/verdicts.go:MarkVerdictEvidencePublished", Reason: "marks the internal accepted receipt's evidence delivery; the verdict commit already announced the run projection"},
	{Key: "internal/workflow/persistence/verdicts.go:ResolveVerdictOperationDiverged", Reason: "terminally resolves an internal recovery receipt without changing the run; diagnostic recovery state is not a workflow wire transition"},
	{Key: "internal/workflow/persistence/verdicts.go:RebaseVerdictOperation", Reason: "moves a prepared verdict operation, an internal recovery receipt, onto a newer phase revision; the verdict announces when it commits"},
}

// EventTables are the tables an announcing function writes, plus the floor.
func EventTables(r *laws.Run) map[string]bool {
	return scanOf(r, lycaonOutbox).eventTables
}

// Each function that writes an event-carrying table enqueues the transition,
// reaches an enqueue in its own transaction scope, or runs under a caller
// that does.
func mutatorsAnnounceTheirTransition(r *laws.Run) {
	scan := scanOf(r, lycaonOutbox)
	silent := scan.silentMutators()
	for _, key := range sortedKeys(silent) {
		if !r.Exempt(key) {
			r.Report("%s writes %s and announces nothing; enqueue the transition with EnqueueTx in the transaction that makes it durable", key, scan.describeTables(silent[key]))
		}
	}
}

func enqueuesTakeAMutationTransaction(r *laws.Run) {
	scan := scanOf(r, lycaonOutbox)
	r.Expect(len(scan.enqueueSites), "EnqueueTx call sites")
	for _, site := range scan.enqueueSites {
		if !site.heldTx {
			r.Report("%s: enqueues through %s, not a transaction the function holds", site.where, site.arg)
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
