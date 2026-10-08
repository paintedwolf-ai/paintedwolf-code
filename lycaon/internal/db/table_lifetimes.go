package db

// TableLifetimeClass states how rows of a schema table leave the store.
type TableLifetimeClass string

const (
	// LifetimeCascadeOwned rows leave when their owner row is deleted.
	LifetimeCascadeOwned TableLifetimeClass = "cascade-owned"
	// LifetimeForever rows are durable roots that normal operation never deletes.
	LifetimeForever TableLifetimeClass = "forever"
	// LifetimeReceiptWithRetention rows are journals or receipts pruned after a retention cutoff.
	LifetimeReceiptWithRetention TableLifetimeClass = "receipt-with-retention"
	// LifetimeProjection rows are derived and rebuildable from source rows.
	LifetimeProjection TableLifetimeClass = "projection"
	// LifetimeCache rows are scratch, in-flight coordination, or re-scannable entries.
	LifetimeCache TableLifetimeClass = "cache"
)

// TableLifetime classifies one schema table. A cascade-owned table names the
// parent table whose ON DELETE CASCADE foreign key removes its rows; every
// other class carries a reason.
type TableLifetime struct {
	Class  TableLifetimeClass
	Owner  string
	Reason string
}

func cascadeOwnedBy(owner string) TableLifetime {
	return TableLifetime{Class: LifetimeCascadeOwned, Owner: owner}
}

var tableLifetimes = map[string]TableLifetime{
	"projects":              {Class: LifetimeForever, Reason: "top-level project records"},
	"people":                {Class: LifetimeForever, Reason: "identity and person records"},
	"schema_migrations":     {Class: LifetimeForever, Reason: "schema revision history"},
	"store_meta":            {Class: LifetimeForever, Reason: "store initialization metadata"},
	"history_storage_clock": {Class: LifetimeForever, Reason: "global monotonic storage clock"},
	"managed_secrets":       {Class: LifetimeForever, Reason: "secret credential identity records"},
	"person_actions":        {Class: LifetimeForever, Reason: "record of API operations people invoked"},

	"approval_operations":          {Class: LifetimeReceiptWithRetention, Reason: "human approval decision receipts"},
	"command_invocations":          {Class: LifetimeReceiptWithRetention, Reason: "execution plane command journals"},
	"project_removals":             {Class: LifetimeReceiptWithRetention, Reason: "project deletion receipts"},
	"rewind_operations":            {Class: LifetimeReceiptWithRetention, Reason: "filesystem rewind journals"},
	"workflow_commands":            {Class: LifetimeReceiptWithRetention, Reason: "workflow command receipts"},
	"workflow_start_operations":    {Class: LifetimeReceiptWithRetention, Reason: "workflow start receipts"},
	"workflow_teardown_operations": {Class: LifetimeReceiptWithRetention, Reason: "workflow teardown journals"},
	"workflow_verdict_operations":  {Class: LifetimeReceiptWithRetention, Reason: "review verdict receipts"},
	"prompt_submissions":           {Class: LifetimeReceiptWithRetention, Reason: "prompt dispatch receipts"},
	"source_file_requests":         {Class: LifetimeReceiptWithRetention, Reason: "file operation receipts"},
	"source_mutations":             {Class: LifetimeReceiptWithRetention, Reason: "source mutation journals"},
	"editor_mutations":             {Class: LifetimeReceiptWithRetention, Reason: "editor mutation receipts"},
	"llm_calls":                    {Class: LifetimeReceiptWithRetention, Reason: "provider call receipts"},
	"artifact_gc_queue":            {Class: LifetimeReceiptWithRetention, Reason: "artifact collection worklist"},
	"content_blob_reclaim_queue":   {Class: LifetimeReceiptWithRetention, Reason: "content blob reclaim worklist"},
	"source_blob_reclaim_queue":    {Class: LifetimeReceiptWithRetention, Reason: "source blob reclaim worklist"},
	"extension_operations":         {Class: LifetimeReceiptWithRetention, Reason: "extension install and update receipts"},
	"editor_agent_receipts":        {Class: LifetimeReceiptWithRetention, Reason: "editor agent receipts"},
	"editor_replica_receipts":      {Class: LifetimeReceiptWithRetention, Reason: "editor replica update receipts"},
	"invocation_receipts":          {Class: LifetimeReceiptWithRetention, Reason: "tool invocation receipts"},
	"editor_document_retention":    {Class: LifetimeReceiptWithRetention, Reason: "document retention records"},

	"file_briefings":                 {Class: LifetimeProjection, Reason: "briefings derived from files"},
	"chunk_projections":              {Class: LifetimeProjection, Reason: "search chunks derived from messages"},
	"model_output_projections":       {Class: LifetimeProjection, Reason: "slices derived from model output streams"},
	"evidence_index":                 {Class: LifetimeProjection, Reason: "search index over evidence"},
	"fts_maintenance":                {Class: LifetimeProjection, Reason: "full-text index maintenance state"},
	"llm_call_rollups":               {Class: LifetimeProjection, Reason: "spend and token rollups"},
	"llm_cost_totals":                {Class: LifetimeProjection, Reason: "spend totals by provider and model"},
	"code_scan_page_ordinals":        {Class: LifetimeProjection, Reason: "scan pagination ordinals"},
	"workflow_run_page_ordinals":     {Class: LifetimeProjection, Reason: "workflow run pagination ordinals"},
	"scan_finding_rollups":           {Class: LifetimeProjection, Reason: "finding rollups"},
	"assessment_finding_rollups":     {Class: LifetimeProjection, Reason: "assessment rollups"},
	"scan_finding_ledger":            {Class: LifetimeProjection, Reason: "finding states derived from finding events"},
	"scan_finding_events":            {Class: LifetimeProjection, Reason: "finding lifecycle events"},
	"scan_finding_entries":           {Class: LifetimeProjection, Reason: "finding set entries"},
	"scan_finding_sets":              {Class: LifetimeProjection, Reason: "finding sets per scan"},
	"scan_summaries":                 {Class: LifetimeProjection, Reason: "scan summaries"},
	"scan_run_facts":                 {Class: LifetimeProjection, Reason: "per-scan metrics"},
	"scan_secret_identities":         {Class: LifetimeProjection, Reason: "deduplicated secret identities"},
	"scan_comparisons":               {Class: LifetimeProjection, Reason: "diffs between scan runs"},
	"scan_blob_findings":             {Class: LifetimeProjection, Reason: "finding links to blobs"},
	"source_line_attr":               {Class: LifetimeProjection, Reason: "per-line attribution intervals"},
	"source_presentation_watermarks": {Class: LifetimeProjection, Reason: "source view presentation watermarks"},
	"source_inventory_state":         {Class: LifetimeProjection, Reason: "source tree inventory state"},
	"source_version_text_states":     {Class: LifetimeProjection, Reason: "text states across versions"},
	"source_text_identity_ranges":    {Class: LifetimeProjection, Reason: "identity ranges across source text"},
	"source_text_contributions":      {Class: LifetimeProjection, Reason: "text contribution records"},
	"source_effect_contributions":    {Class: LifetimeProjection, Reason: "effect contribution records"},

	"code_scans":                 {Class: LifetimeCache, Reason: "re-scannable code scan jobs and results"},
	"scan_series":                {Class: LifetimeCache, Reason: "scan grouping sequences"},
	"scan_ignore_state":          {Class: LifetimeCache, Reason: "last applied ignore catalog digest"},
	"security_assessments":       {Class: LifetimeCache, Reason: "re-evaluable security assessments"},
	"security_full_passes":       {Class: LifetimeCache, Reason: "full-pass security evaluations"},
	"call_reservations":          {Class: LifetimeCache, Reason: "in-flight provider call reservations"},
	"wait_leases":                {Class: LifetimeCache, Reason: "wait queue leases"},
	"event_outbox":               {Class: LifetimeCache, Reason: "events awaiting delivery"},
	"history_pruned_bodies":      {Class: LifetimeCache, Reason: "pruned transcript bodies"},
	"source_manifest_staging":    {Class: LifetimeCache, Reason: "in-flight source manifest staging"},
	"source_manifest_chunks":     {Class: LifetimeCache, Reason: "source manifest chunks"},
	"source_snapshots":           {Class: LifetimeCache, Reason: "re-computable source tree snapshots"},
	"source_blob_objects":        {Class: LifetimeCache, Reason: "content-addressed blob metadata"},
	"session_spend_warnings":     {Class: LifetimeCache, Reason: "spend warning deduplication"},
	"editor_replica_heads":       {Class: LifetimeCache, Reason: "editor replica head pointers"},
	"editor_save_pins":           {Class: LifetimeCache, Reason: "in-flight save pins"},
	"source_branch_heads":        {Class: LifetimeCache, Reason: "branch tip pointers"},
	"source_git_heads":           {Class: LifetimeCache, Reason: "git head references"},
	"source_git_transitions":     {Class: LifetimeCache, Reason: "git commit transitions"},
	"source_snapshot_heads":      {Class: LifetimeCache, Reason: "snapshot head pointers"},
	"source_snapshot_roots":      {Class: LifetimeCache, Reason: "snapshot root references"},
	"source_snapshot_boundaries": {Class: LifetimeCache, Reason: "snapshot boundary markers"},
	"source_snapshot_chunks":     {Class: LifetimeCache, Reason: "snapshot chunk references"},

	"artifact_refs":                  cascadeOwnedBy("artifacts"),
	"artifacts":                      cascadeOwnedBy("projects"),
	"assessment_scan_bindings":       cascadeOwnedBy("security_assessments"),
	"authorization_contexts":         cascadeOwnedBy("sessions"),
	"authz_events":                   cascadeOwnedBy("sessions"),
	"blueprint_approvals":            cascadeOwnedBy("projects"),
	"chat_grants":                    cascadeOwnedBy("sessions"),
	"checkpoint_anchors":             cascadeOwnedBy("sessions"),
	"checkpoint_decision_stamps":     cascadeOwnedBy("sessions"),
	"checkpoint_object_refs":         cascadeOwnedBy("checkpoint_anchors"),
	"checkpoint_resolution_ordinals": cascadeOwnedBy("checkpoints"),
	"checkpoints":                    cascadeOwnedBy("sessions"),
	"compaction_attempts":            cascadeOwnedBy("sessions"),
	"compaction_spill_refs":          cascadeOwnedBy("sessions"),
	"compaction_views":               cascadeOwnedBy("sessions"),
	"content_blob_objects":           cascadeOwnedBy("projects"),
	"credential_authored_values":     cascadeOwnedBy("projects"),
	"delegations":                    cascadeOwnedBy("sessions"),
	"delegation_legs":                cascadeOwnedBy("delegations"),
	"draft_versions":                 cascadeOwnedBy("sessions"),
	"editor_document_changes":        cascadeOwnedBy("editor_documents"),
	"editor_document_snapshots":      cascadeOwnedBy("editor_documents"),
	"editor_documents":               cascadeOwnedBy("projects"),
	"editor_replica_updates":         cascadeOwnedBy("editor_documents"),
	"editor_replicas":                cascadeOwnedBy("editor_documents"),
	"editor_retargets":               cascadeOwnedBy("projects"),
	"evidence_records":               cascadeOwnedBy("sessions"),
	"findings":                       cascadeOwnedBy("sessions"),
	"full_pass_session_bindings":     cascadeOwnedBy("sessions"),
	"full_pass_workflow_bindings":    cascadeOwnedBy("workflow_runs"),
	"history_protections":            cascadeOwnedBy("sessions"),
	"landed_changes":                 cascadeOwnedBy("worker_jobs"),
	"live_model_outputs":             cascadeOwnedBy("turn_attempts"),
	"managed_cookie_jar_saves":       cascadeOwnedBy("managed_secrets"),
	"managed_secret_reveals":         cascadeOwnedBy("managed_secrets"),
	"managed_secret_uses":            cascadeOwnedBy("managed_secrets"),
	"managed_secret_versions":        cascadeOwnedBy("managed_secrets"),
	"managed_token_jar_saves":        cascadeOwnedBy("managed_secrets"),
	"message_attachment_refs":        cascadeOwnedBy("messages"),
	"message_spill_refs":             cascadeOwnedBy("messages"),
	"messages":                       cascadeOwnedBy("sessions"),
	"model_outputs":                  cascadeOwnedBy("turn_attempts"),
	"project_promotions":             cascadeOwnedBy("projects"),
	"project_roots":                  cascadeOwnedBy("projects"),
	"project_trust_baselines":        cascadeOwnedBy("projects"),
	"prompt_attachment_admissions":   cascadeOwnedBy("prompt_submissions"),
	"prompt_attachment_blobs":        cascadeOwnedBy("projects"),
	"session_entries":                cascadeOwnedBy("sessions"),
	"session_model_limits":           cascadeOwnedBy("sessions"),
	"session_progress":               cascadeOwnedBy("sessions"),
	"session_scan_bindings":          cascadeOwnedBy("sessions"),
	"session_source_turns":           cascadeOwnedBy("sessions"),
	"session_turn_heads":             cascadeOwnedBy("sessions"),
	"session_workflow_scaffold":      cascadeOwnedBy("sessions"),
	"session_workflows":              cascadeOwnedBy("sessions"),
	"session_worktrees":              cascadeOwnedBy("sessions"),
	"sessions":                       cascadeOwnedBy("projects"),
	"source_agent_presentations":     cascadeOwnedBy("projects"),
	"source_checkpoint_entries":      cascadeOwnedBy("source_checkpoints"),
	"source_checkpoint_git_states":   cascadeOwnedBy("source_checkpoints"),
	"source_checkpoints":             cascadeOwnedBy("projects"),
	"source_command_window_objects":  cascadeOwnedBy("source_command_windows"),
	"source_command_windows":         cascadeOwnedBy("projects"),
	"source_effects":                 cascadeOwnedBy("projects"),
	"source_files":                   cascadeOwnedBy("projects"),
	"source_history_entries":         cascadeOwnedBy("projects"),
	"source_manifest_entries":        cascadeOwnedBy("source_manifest_chunks"),
	"source_operations":              cascadeOwnedBy("projects"),
	"source_recovery_entries":        cascadeOwnedBy("projects"),
	"source_recovery_objects":        cascadeOwnedBy("projects"),
	"source_versions":                cascadeOwnedBy("projects"),
	"source_worktrees":               cascadeOwnedBy("projects"),
	"turn_attempt_closeouts":         cascadeOwnedBy("turn_attempts"),
	"turn_attempts":                  cascadeOwnedBy("turns"),
	"turn_clocks":                    cascadeOwnedBy("sessions"),
	"turn_load_receipts":             cascadeOwnedBy("sessions"),
	"turn_submissions":               cascadeOwnedBy("turns"),
	"turns":                          cascadeOwnedBy("sessions"),
	"vault_unlocks":                  cascadeOwnedBy("projects"),
	"worker_attempts":                cascadeOwnedBy("worker_jobs"),
	"worker_baseline_objects":        cascadeOwnedBy("worker_baselines"),
	"worker_baselines":               cascadeOwnedBy("worker_jobs"),
	"worker_decisions":               cascadeOwnedBy("worker_jobs"),
	"worker_finding_delivery":        cascadeOwnedBy("worker_jobs"),
	"worker_jobs":                    cascadeOwnedBy("projects"),
	"worker_prerequisites":           cascadeOwnedBy("worker_jobs"),
	"worker_outcome_deliveries":      cascadeOwnedBy("worker_jobs"),
	"worker_results":                 cascadeOwnedBy("worker_jobs"),
	"worker_turns":                   cascadeOwnedBy("worker_jobs"),
	"workflow_runs":                  cascadeOwnedBy("sessions"),
	"workflow_run_unit_provenance":   cascadeOwnedBy("workflow_runs"),
	"workflow_scan_bindings":         cascadeOwnedBy("workflow_runs"),
}

// TableLifetimes returns a copy of the lifetime of every schema table.
func TableLifetimes() map[string]TableLifetime {
	out := make(map[string]TableLifetime, len(tableLifetimes))
	for table, lifetime := range tableLifetimes {
		out[table] = lifetime
	}
	return out
}
