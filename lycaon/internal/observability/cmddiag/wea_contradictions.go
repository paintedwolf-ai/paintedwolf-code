package cmddiag

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Rule IDs for Workers, Edits, and Authority (WEA) ordinary-development
// contradiction diagnostics (TB11).
const (
	RuleWEALandedPathMismatch        RuleID = "wea_landed_path_mismatch"
	RuleWEAEditBasisOrAuthorMismatch RuleID = "wea_edit_basis_or_author_mismatch"
	RuleWEAUnauthorizedEffect        RuleID = "wea_unauthorized_effect"
	RuleWEAUnrecordedOrRefusedEgress RuleID = "wea_unrecorded_or_refused_egress"
	RuleWEASecretCanaryLeak          RuleID = "wea_secret_canary_leak"
)

// WEAObservation is an immutable snapshot of typed durable facts across the
// Workers, Edits, and Authority tracks, joined strictly by typed identities
// (never prose).
type WEAObservation struct {
	SessionID       string `json:"session_id,omitempty"`
	WorkerID        string `json:"worker_id,omitempty"`
	JobID           string `json:"job_id,omitempty"`
	ResultHandle    string `json:"result_handle,omitempty"`
	InvocationID    string `json:"invocation_id,omitempty"`
	ScopeKey        string `json:"scope_key,omitempty"`
	Settled         bool   `json:"settled"`
	RetentionGapped bool   `json:"retention_gapped,omitempty"`

	// Workers track durable facts.
	WorkerResults []WEAWorkerResultFact `json:"worker_results,omitempty"`
	LandedPaths   []WEALandedPathFact   `json:"landed_paths,omitempty"`
	WorkerCards   []WEAWorkerCardFact   `json:"worker_cards,omitempty"`

	// Edits track durable facts.
	ReadBases    []WEAReadBasisFact   `json:"read_bases,omitempty"`
	EditEffects  []WEAEditEffectFact  `json:"edit_effects,omitempty"`
	FileVersions []WEAFileVersionFact `json:"file_versions,omitempty"`

	// Authority track durable facts.
	ApprovalPlans       []WEAApprovalPlanFact     `json:"approval_plans,omitempty"`
	Grants              []WEAGrantFact            `json:"grants,omitempty"`
	AuthorityEffects    []WEAAuthorityEffectFact  `json:"authority_effects,omitempty"`
	EgressDecisions     []WEAEgressDecisionFact   `json:"egress_decisions,omitempty"`
	ObservedConnections []WEAEgressConnectionFact `json:"observed_connections,omitempty"`
	SecretCanaries      []WEASecretCanaryFact     `json:"secret_canaries,omitempty"`
	RecordedArtifacts   []WEARecordedArtifactFact `json:"recorded_artifacts,omitempty"`
}

// WEAWorkerResultFact records a committed worker result handle and its
// host-observed changed paths.
type WEAWorkerResultFact struct {
	ResultHandle string   `json:"result_handle"`
	WorkerID     string   `json:"worker_id,omitempty"`
	JobID        string   `json:"job_id,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	Settled      bool     `json:"settled"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	SourceRef    string   `json:"source_ref,omitempty"`
}

// WEALandedPathFact records a path claimed to have landed from a worker result
// together with optional host-side disk verification.
type WEALandedPathFact struct {
	ResultHandle    string `json:"result_handle"`
	WorkerID        string `json:"worker_id,omitempty"`
	JobID           string `json:"job_id,omitempty"`
	Path            string `json:"path"`
	DiskChecked     bool   `json:"disk_checked,omitempty"`
	PresentOnDisk   bool   `json:"present_on_disk,omitempty"`
	Settled         bool   `json:"settled"`
	RetentionPruned bool   `json:"retention_pruned,omitempty"`
	SourceRef       string `json:"source_ref,omitempty"`
}

// WEAWorkerCardFact captures UI/projection delivery state for a worker result.
// Delayed cards never define or contradict a committed result handle.
type WEAWorkerCardFact struct {
	ResultHandle      string `json:"result_handle"`
	CardDelivered     bool   `json:"card_delivered"`
	CardRevision      int64  `json:"card_revision,omitempty"`
	CommittedRevision int64  `json:"committed_revision,omitempty"`
}

// WEAReadBasisFact records a qualifying read basis for a file revision.
type WEAReadBasisFact struct {
	ReadID          string `json:"read_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	InvocationID    string `json:"invocation_id,omitempty"`
	Path            string `json:"path"`
	Revision        string `json:"revision"`
	Qualifying      bool   `json:"qualifying"`
	Active          bool   `json:"active"`
	RetentionPruned bool   `json:"retention_pruned,omitempty"`
	SourceRef       string `json:"source_ref,omitempty"`
}

// WEAEditEffectFact records an accepted or refused edit effect.
type WEAEditEffectFact struct {
	EffectID            string `json:"effect_id"`
	VersionID           string `json:"version_id,omitempty"`
	SessionID           string `json:"session_id,omitempty"`
	WorkerID            string `json:"worker_id,omitempty"`
	InvocationID        string `json:"invocation_id,omitempty"`
	Path                string `json:"path"`
	Accepted            bool   `json:"accepted"`
	Refused             bool   `json:"refused,omitempty"`
	RefusalCode         string `json:"refusal_code,omitempty"`
	ReadBasisRevision   string `json:"read_basis_revision,omitempty"`
	CurrentRevision     string `json:"current_revision,omitempty"`
	RebasedCleanly      bool   `json:"rebased_cleanly,omitempty"`
	RebasedFromRevision string `json:"rebased_from_revision,omitempty"`
	AuthorID            string `json:"author_id,omitempty"`
	AuthorKind          string `json:"author_kind,omitempty"`
	RetentionPruned     bool   `json:"retention_pruned,omitempty"`
	SourceRef           string `json:"source_ref,omitempty"`
}

// WEAFileVersionFact records a durable file version entry linked to an effect.
type WEAFileVersionFact struct {
	VersionID      string `json:"version_id"`
	EffectID       string `json:"effect_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	WorkerID       string `json:"worker_id,omitempty"`
	Path           string `json:"path,omitempty"`
	AuthorID       string `json:"author_id,omitempty"`
	AuthorKind     string `json:"author_kind,omitempty"`
	BaseRevision   string `json:"base_revision,omitempty"`
	ResultRevision string `json:"result_revision,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
}

// WEAApprovalPlanFact records a durable approval plan decision.
type WEAApprovalPlanFact struct {
	ApprovalPlanID string `json:"approval_plan_id"`
	SessionID      string `json:"session_id,omitempty"`
	InvocationID   string `json:"invocation_id,omitempty"`
	ActionKind     string `json:"action_kind,omitempty"`
	TargetRef      string `json:"target_ref,omitempty"`
	ActionDigest   string `json:"action_digest,omitempty"`
	Approved       bool   `json:"approved"`
	Refused        bool   `json:"refused,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
}

// WEAGrantFact records an installed reusable authority grant.
type WEAGrantFact struct {
	GrantID      string `json:"grant_id"`
	SessionID    string `json:"session_id,omitempty"`
	Scope        string `json:"scope,omitempty"` // "exact", "chat", "project", "device"
	ActionKind   string `json:"action_kind,omitempty"`
	TargetRef    string `json:"target_ref,omitempty"`
	ActionDigest string `json:"action_digest,omitempty"`
	Active       bool   `json:"active"`
	Revoked      bool   `json:"revoked,omitempty"`
	Expired      bool   `json:"expired,omitempty"`
	SourceRef    string `json:"source_ref,omitempty"`
}

// WEAAuthorityEffectFact records an authority-gated effect execution or refusal.
type WEAAuthorityEffectFact struct {
	EffectID          string `json:"effect_id"`
	SessionID         string `json:"session_id,omitempty"`
	WorkerID          string `json:"worker_id,omitempty"`
	InvocationID      string `json:"invocation_id,omitempty"`
	ActionKind        string `json:"action_kind,omitempty"`
	TargetRef         string `json:"target_ref,omitempty"`
	ActionDigest      string `json:"action_digest,omitempty"`
	ApprovalPlanID    string `json:"approval_plan_id,omitempty"`
	GrantID           string `json:"grant_id,omitempty"`
	RequiresAuthority bool   `json:"requires_authority"`
	Executed          bool   `json:"executed"`
	Refused           bool   `json:"refused,omitempty"`
	RefusalCode       string `json:"refusal_code,omitempty"`
	RetentionPruned   bool   `json:"retention_pruned,omitempty"`
	SourceRef         string `json:"source_ref,omitempty"`
}

// WEAEgressDecisionFact records a product-side egress gate decision.
type WEAEgressDecisionFact struct {
	DecisionID      string `json:"decision_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	WorkerID        string `json:"worker_id,omitempty"`
	InvocationID    string `json:"invocation_id,omitempty"`
	DestinationRef  string `json:"destination_ref"`
	Admitted        bool   `json:"admitted"`
	Refused         bool   `json:"refused,omitempty"`
	RefusalCode     string `json:"refusal_code,omitempty"`
	DecisionOrdinal int64  `json:"decision_ordinal,omitempty"`
	SourceRef       string `json:"source_ref,omitempty"`
}

// WEAEgressConnectionFact records a host/fixture-observed egress connection.
type WEAEgressConnectionFact struct {
	ConnectionID    string `json:"connection_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	WorkerID        string `json:"worker_id,omitempty"`
	InvocationID    string `json:"invocation_id,omitempty"`
	DestinationRef  string `json:"destination_ref"`
	ObservedOrdinal int64  `json:"observed_ordinal,omitempty"`
	Settled         bool   `json:"settled"`
	RetentionPruned bool   `json:"retention_pruned,omitempty"`
	SourceRef       string `json:"source_ref,omitempty"`
}

// WEASecretCanaryFact identifies a synthetic secret canary (D6). CanaryValue is
// used strictly in memory for exact-byte screening and is never emitted.
type WEASecretCanaryFact struct {
	CanaryID     string `json:"canary_id"`
	SessionID    string `json:"session_id,omitempty"`
	Scope        string `json:"scope,omitempty"`
	CanaryDigest string `json:"canary_digest,omitempty"`
	CanaryValue  string `json:"-"`
}

// WEARecordedArtifactFact represents an agent-visible or persisted record
// checked for secret custody leaks (D6/D7). ContentBytes is never emitted.
type WEARecordedArtifactFact struct {
	RecordID              string `json:"record_id"`
	SessionID             string `json:"session_id,omitempty"`
	WorkerID              string `json:"worker_id,omitempty"`
	JobID                 string `json:"job_id,omitempty"`
	ResultHandle          string `json:"result_handle,omitempty"`
	EffectID              string `json:"effect_id,omitempty"`
	InvocationID          string `json:"invocation_id,omitempty"`
	RecordKind            string `json:"record_kind,omitempty"`
	ScreenedReferenceOnly bool   `json:"screened_reference_only,omitempty"`
	CanaryDetected        bool   `json:"canary_detected,omitempty"`
	MatchedCanaryID       string `json:"matched_canary_id,omitempty"`
	ContentBytes          string `json:"-"`
	SourceRef             string `json:"source_ref,omitempty"`
}

// WEATask is a deferred bounded fetch task for WEA observations.
type WEATask struct {
	SessionID string
	ScopeKey  string
	Fetch     func(ctx context.Context) (WEAObservation, error)
}

// WEAConfig configures an opt-in bounded asynchronous WEA diagnostic observer.
type WEAConfig struct {
	Enabled       bool
	QueueCapacity int
	TaskTimeout   time.Duration
	CloseGrace    time.Duration
	MaxFindings   int
	Sink          func(Finding)
	Now           func() time.Time
}

// WEAReport is a bounded, deduplicated local summary of emitted WEA findings.
type WEAReport struct {
	SchemaVersion int       `json:"schema_version"`
	Findings      []Finding `json:"findings,omitempty"`
	Stats         Stats     `json:"stats"`
}

type weaWorkItem struct {
	obs  *WEAObservation
	task *WEATask
}

// WEAObserver evaluates WEA observations asynchronously on a bounded queue.
// When disabled, it starts no goroutines, allocates no queue or dedup tables,
// and Submit/SubmitTask are O(1) no-ops.
type WEAObserver struct {
	enabled     bool
	capacity    int
	taskTimeout time.Duration
	closeGrace  time.Duration
	maxFindings int
	sink        func(Finding)
	now         func() time.Time

	workerCtx    context.Context
	workerCancel context.CancelFunc

	mu               sync.RWMutex
	closed           bool
	queue            chan weaWorkItem
	wg               sync.WaitGroup
	closeOnce        sync.Once
	seen             map[dedupKey]int
	findingsMu       sync.Mutex
	findings         []Finding
	enqueued         atomic.Int64
	dropped          atomic.Int64
	emitted          atomic.Int64
	deduplicated     atomic.Int64
	reportedDropMark atomic.Int64
}

// NewWEAObserver creates a bounded opt-in WEA observer. When cfg.Enabled is
// false, no background goroutine, queue, or map is allocated.
func NewWEAObserver(cfg WEAConfig) *WEAObserver {
	if !cfg.Enabled {
		return &WEAObserver{enabled: false}
	}
	capacity := cfg.QueueCapacity
	if capacity <= 0 {
		capacity = DefaultQueueCapacity
	}
	timeout := cfg.TaskTimeout
	if timeout <= 0 {
		timeout = DefaultTaskTimeout
	}
	grace := cfg.CloseGrace
	if grace <= 0 {
		grace = DefaultCloseGrace
	}
	maxFindings := cfg.MaxFindings
	if maxFindings <= 0 {
		maxFindings = DefaultMaxDedupEntries
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	workerCtx, workerCancel := context.WithCancel(context.Background())
	obs := &WEAObserver{
		enabled:      true,
		capacity:     capacity,
		taskTimeout:  timeout,
		closeGrace:   grace,
		maxFindings:  maxFindings,
		sink:         cfg.Sink,
		now:          nowFn,
		workerCtx:    workerCtx,
		workerCancel: workerCancel,
		queue:        make(chan weaWorkItem, capacity),
		seen:         make(map[dedupKey]int),
	}
	obs.wg.Add(1)
	go obs.run()
	return obs
}

// Enabled reports whether this WEA observer is active.
func (o *WEAObserver) Enabled() bool {
	return o != nil && o.enabled
}

// Submit enqueues an immutable WEA observation without blocking.
// When disabled, this is an O(1), zero-allocation no-op.
func (o *WEAObserver) Submit(obs WEAObservation) bool {
	if o == nil || !o.enabled {
		return false
	}
	copyObs := obs
	return o.enqueue(weaWorkItem{obs: &copyObs})
}

// SubmitTask enqueues a deferred fetch task without blocking.
// When disabled or saturated, task.Fetch is never called.
func (o *WEAObserver) SubmitTask(task WEATask) bool {
	if o == nil || !o.enabled || task.Fetch == nil {
		return false
	}
	copyTask := task
	return o.enqueue(weaWorkItem{task: &copyTask})
}

func (o *WEAObserver) enqueue(item weaWorkItem) bool {
	if o == nil || !o.enabled {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.closed {
		return false
	}
	select {
	case o.queue <- item:
		o.enqueued.Add(1)
		return true
	default:
		o.dropped.Add(1)
		return false
	}
}

// Stats returns the current enqueued, dropped, emitted, and deduplicated counts.
func (o *WEAObserver) Stats() Stats {
	if o == nil {
		return Stats{}
	}
	return Stats{
		Enqueued:     o.enqueued.Load(),
		Dropped:      o.dropped.Load(),
		Emitted:      o.emitted.Load(),
		Deduplicated: o.deduplicated.Load(),
	}
}

// Report returns a bounded, deduplicated snapshot of emitted findings and stats.
func (o *WEAObserver) Report() WEAReport {
	if o == nil || !o.enabled {
		return WEAReport{SchemaVersion: SchemaVersion}
	}
	o.findingsMu.Lock()
	copied := make([]Finding, len(o.findings))
	copy(copied, o.findings)
	o.findingsMu.Unlock()
	return WEAReport{
		SchemaVersion: SchemaVersion,
		Findings:      copied,
		Stats:         o.Stats(),
	}
}

// Close drains queued observations and stops the background worker.
func (o *WEAObserver) Close() {
	if o == nil || !o.enabled {
		return
	}
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closed = true
		close(o.queue)
		o.mu.Unlock()
		done := make(chan struct{})
		go func() {
			o.wg.Wait()
			close(done)
		}()
		timer := time.NewTimer(o.closeGrace)
		select {
		case <-done:
			timer.Stop()
		case <-timer.C:
			if o.workerCancel != nil {
				o.workerCancel()
			}
			<-done
		}
		if o.workerCancel != nil {
			o.workerCancel()
		}
		o.emitOverflowIfNeeded()
	})
}

func (o *WEAObserver) run() {
	defer o.wg.Done()
	for item := range o.queue {
		o.emitOverflowIfNeeded()
		var obs WEAObservation
		if item.task != nil {
			taskCtx, cancel := context.WithTimeout(o.workerCtx, o.taskTimeout)
			fetched, err := item.task.Fetch(taskCtx)
			cancel()
			if err != nil {
				o.emitDedup(sanitizeFinding(Finding{
					SchemaVersion: SchemaVersion,
					Timestamp:     o.now().UTC(),
					RuleID:        RuleObservationLoss,
					Disposition:   DispositionIncomplete,
					SessionID:     item.task.SessionID,
					ProgramID:     item.task.ScopeKey,
					Expected:      map[string]any{"wea_fact_read_succeeded": true},
					Observed:      map[string]any{"wea_fact_read_succeeded": false},
					Reason:        "wea_fact_read_failed",
				}))
				continue
			}
			obs = fetched
		} else if item.obs != nil {
			obs = *item.obs
		}
		for _, finding := range EvaluateWEA(obs, o.now()) {
			o.emitDedup(finding)
		}
	}
}

func (o *WEAObserver) emitDedup(f Finding) {
	if f.RuleID != RuleCaptureOverflow && f.ProgramID != "" {
		key := dedupKey{rule: f.RuleID, programID: f.ProgramID}
		rank := dispositionRank(f.Disposition)
		if prevRank, exists := o.seen[key]; exists && rank <= prevRank {
			o.deduplicated.Add(1)
			return
		}
		if len(o.seen) >= DefaultMaxDedupEntries {
			clear(o.seen)
		}
		o.seen[key] = rank
	}
	o.emit(f)
}

func (o *WEAObserver) emitOverflowIfNeeded() {
	dropped := o.dropped.Load()
	prev := o.reportedDropMark.Load()
	if dropped <= prev {
		return
	}
	if !o.reportedDropMark.CompareAndSwap(prev, dropped) {
		return
	}
	f := sanitizeFinding(Finding{
		SchemaVersion: SchemaVersion,
		Timestamp:     o.now().UTC(),
		RuleID:        RuleCaptureOverflow,
		Disposition:   DispositionOverflow,
		DroppedCount:  dropped,
		Expected:      map[string]any{"dropped_observations": 0, "queue_capacity": o.capacity},
		Observed:      map[string]any{"dropped_observations": dropped},
		Reason:        "bounded_queue_saturated",
	})
	o.emit(f)
}

func (o *WEAObserver) emit(f Finding) {
	o.emitted.Add(1)
	o.findingsMu.Lock()
	if len(o.findings) < o.maxFindings {
		o.findings = append(o.findings, f)
	}
	o.findingsMu.Unlock()
	if o.sink != nil {
		o.sink(f)
	}
}

// EvaluateWEAIfEnabled runs EvaluateWEA only when enabled is true.
// When disabled, it returns nil immediately with zero allocations.
func EvaluateWEAIfEnabled(enabled bool, obs WEAObservation, now time.Time) []Finding {
	if !enabled {
		return nil
	}
	return EvaluateWEA(obs, now)
}

// EvaluateWEA runs all five Workers, Edits, and Authority contradiction checks
// against an immutable WEAObservation. At most one sanitized finding is emitted
// per rule per observation, and all secret canaries and raw paths are redacted.
func EvaluateWEA(obs WEAObservation, now time.Time) []Finding {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var out []Finding
	if f, ok := checkWEALandedPathMismatch(obs, now); ok {
		out = append(out, finalizeWEAFinding(f, obs.SecretCanaries))
	}
	if f, ok := checkWEAEditBasisOrAuthorMismatch(obs, now); ok {
		out = append(out, finalizeWEAFinding(f, obs.SecretCanaries))
	}
	if f, ok := checkWEAUnauthorizedEffect(obs, now); ok {
		out = append(out, finalizeWEAFinding(f, obs.SecretCanaries))
	}
	if f, ok := checkWEAUnrecordedOrRefusedEgress(obs, now); ok {
		out = append(out, finalizeWEAFinding(f, obs.SecretCanaries))
	}
	if f, ok := checkWEASecretCanaryLeak(obs, now); ok {
		out = append(out, finalizeWEAFinding(f, obs.SecretCanaries))
	}
	return out
}

func weaScopeKey(obs WEAObservation) string {
	for _, candidate := range []string{obs.ScopeKey, obs.ResultHandle, obs.JobID, obs.InvocationID, obs.WorkerID, obs.SessionID} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func baseWEAFinding(obs WEAObservation, now time.Time, rule RuleID, disp Disposition) Finding {
	return Finding{
		SchemaVersion: SchemaVersion,
		Timestamp:     now.UTC(),
		RuleID:        rule,
		Disposition:   disp,
		SessionID:     obs.SessionID,
		ProgramID:     weaScopeKey(obs),
		ToolCallID:    obs.InvocationID,
	}
}

func finalizeWEAFinding(f Finding, canaries []WEASecretCanaryFact) Finding {
	sanitized := sanitizeFinding(f)
	if len(canaries) == 0 {
		return sanitized
	}
	secrets := make([]string, 0, len(canaries))
	for _, c := range canaries {
		if c.CanaryValue != "" {
			secrets = append(secrets, c.CanaryValue)
		}
	}
	if len(secrets) == 0 {
		return sanitized
	}
	sanitized.SessionID = scrubStrings(sanitized.SessionID, secrets)
	sanitized.ProgramID = scrubStrings(sanitized.ProgramID, secrets)
	sanitized.ParentReceipt = scrubStrings(sanitized.ParentReceipt, secrets)
	sanitized.ToolCallID = scrubStrings(sanitized.ToolCallID, secrets)
	sanitized.Reason = scrubStrings(sanitized.Reason, secrets)
	sanitized.ExecutionIDs = scrubSlice(sanitized.ExecutionIDs, secrets)
	sanitized.OperationIDs = scrubSlice(sanitized.OperationIDs, secrets)
	sanitized.SourceIDs = scrubSlice(sanitized.SourceIDs, secrets)
	sanitized.Expected = scrubMap(sanitized.Expected, secrets)
	sanitized.Observed = scrubMap(sanitized.Observed, secrets)
	return sanitized
}

func scrubStrings(s string, secrets []string) string {
	if s == "" {
		return s
	}
	out := s
	for _, sec := range secrets {
		if sec != "" && strings.Contains(out, sec) {
			out = strings.ReplaceAll(out, sec, "[REDACTED]")
		}
	}
	return out
}

func scrubSlice(in []string, secrets []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = scrubStrings(v, secrets)
	}
	return out
}

func scrubMap(in map[string]any, secrets []string) map[string]any {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		cleanKey := scrubStrings(k, secrets)
		switch x := v.(type) {
		case string:
			out[cleanKey] = scrubStrings(x, secrets)
		case []string:
			out[cleanKey] = scrubSlice(x, secrets)
		case map[string]any:
			out[cleanKey] = scrubMap(x, secrets)
		default:
			out[cleanKey] = v
		}
	}
	return out
}

func normalizePathKey(p string) string {
	trimmed := strings.TrimSpace(p)
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	for strings.HasPrefix(trimmed, "./") {
		trimmed = strings.TrimPrefix(trimmed, "./")
	}
	return trimmed
}

// 1. Landed path absent from committed worker result (or claimed landed path missing on disk).
func checkWEALandedPathMismatch(obs WEAObservation, now time.Time) (Finding, bool) {
	if len(obs.LandedPaths) == 0 || obs.RetentionGapped {
		return Finding{}, false
	}
	resultsByHandle := make(map[string]WEAWorkerResultFact, len(obs.WorkerResults))
	changedSets := make(map[string]map[string]struct{}, len(obs.WorkerResults))
	for _, res := range obs.WorkerResults {
		if res.ResultHandle == "" {
			continue
		}
		resultsByHandle[res.ResultHandle] = res
		set := make(map[string]struct{}, len(res.ChangedPaths))
		for _, p := range res.ChangedPaths {
			if norm := normalizePathKey(p); norm != "" {
				set[norm] = struct{}{}
			}
		}
		changedSets[res.ResultHandle] = set
	}

	var reasons, handles, workerIDs, jobIDs, pathDigests, sourceRefs []string
	for _, lp := range obs.LandedPaths {
		if lp.RetentionPruned || (!obs.Settled && !lp.Settled) {
			continue
		}
		normPath := normalizePathKey(lp.Path)
		pathDigest := DigestReference(normPath)
		if lp.DiskChecked && !lp.PresentOnDisk {
			reasons = append(reasons, "landed_path_missing_on_disk")
			handles = append(handles, lp.ResultHandle)
			workerIDs = append(workerIDs, lp.WorkerID)
			jobIDs = append(jobIDs, lp.JobID)
			pathDigests = append(pathDigests, pathDigest)
			sourceRefs = append(sourceRefs, DigestReference(lp.SourceRef), pathDigest)
			continue
		}
		if lp.ResultHandle == "" {
			continue
		}
		res, ok := resultsByHandle[lp.ResultHandle]
		if !ok {
			if obs.Settled && lp.Settled {
				reasons = append(reasons, "landed_path_missing_worker_result")
				handles = append(handles, lp.ResultHandle)
				workerIDs = append(workerIDs, lp.WorkerID)
				jobIDs = append(jobIDs, lp.JobID)
				pathDigests = append(pathDigests, pathDigest)
				sourceRefs = append(sourceRefs, DigestReference(lp.SourceRef), pathDigest)
			}
			continue
		}
		if !res.Settled {
			// Healthy lag while worker result is not yet settled.
			continue
		}
		if _, found := changedSets[lp.ResultHandle][normPath]; !found {
			reasons = append(reasons, "landed_path_absent_from_worker_result")
			handles = append(handles, lp.ResultHandle)
			workerIDs = append(workerIDs, lp.WorkerID, res.WorkerID)
			jobIDs = append(jobIDs, lp.JobID, res.JobID)
			pathDigests = append(pathDigests, pathDigest)
			sourceRefs = append(sourceRefs, DigestReference(lp.SourceRef), DigestReference(res.SourceRef), pathDigest)
		}
	}
	if len(reasons) == 0 {
		return Finding{}, false
	}
	slices.Sort(reasons)
	reasons = slices.Compact(reasons)
	f := baseWEAFinding(obs, now, RuleWEALandedPathMismatch, DispositionContradiction)
	f.OperationIDs = handles
	f.SourceIDs = sourceRefs
	f.Reason = reasons[0]
	f.Expected = map[string]any{
		"landed_paths_in_committed_result": true,
		"landed_paths_present_on_disk":     true,
	}
	f.Observed = map[string]any{
		"mismatches":     reasons,
		"result_handles": handles,
		"worker_ids":     workerIDs,
		"job_ids":        jobIDs,
		"path_digests":   pathDigests,
	}
	return f, true
}

// 2. Edit accepted against a stale read basis (or wrong recorded author).
func checkWEAEditBasisOrAuthorMismatch(obs WEAObservation, now time.Time) (Finding, bool) {
	if len(obs.EditEffects) == 0 || obs.RetentionGapped {
		return Finding{}, false
	}

	versionByID := make(map[string]WEAFileVersionFact, len(obs.FileVersions))
	versionByEffect := make(map[string]WEAFileVersionFact, len(obs.FileVersions))
	for _, ver := range obs.FileVersions {
		if ver.VersionID != "" {
			versionByID[ver.VersionID] = ver
		}
		if ver.EffectID != "" {
			versionByEffect[ver.EffectID] = ver
		}
	}

	hasQualifyingRead := func(sessionID, path, rev string) bool {
		if rev == "" {
			return false
		}
		normPath := normalizePathKey(path)
		for _, rb := range obs.ReadBases {
			if rb.RetentionPruned {
				return true
			}
			if rb.SessionID != "" && sessionID != "" && rb.SessionID != sessionID {
				continue
			}
			if normalizePathKey(rb.Path) != normPath {
				continue
			}
			if rb.Revision == rev && rb.Qualifying && rb.Active {
				return true
			}
		}
		return false
	}

	var reasons, effectIDs, versionIDs, workerIDs, sourceRefs []string
	for _, ed := range obs.EditEffects {
		if ed.RetentionPruned || !ed.Accepted || ed.Refused {
			continue
		}
		sessionID := ed.SessionID
		if sessionID == "" {
			sessionID = obs.SessionID
		}
		pathDigest := DigestReference(normalizePathKey(ed.Path))

		// Check read basis validity.
		if ed.RebasedCleanly {
			basisRev := ed.RebasedFromRevision
			if basisRev == "" {
				basisRev = ed.ReadBasisRevision
			}
			if len(obs.ReadBases) > 0 && !hasQualifyingRead(sessionID, ed.Path, basisRev) {
				reasons = append(reasons, "rebased_edit_missing_qualifying_read_basis")
				effectIDs = append(effectIDs, ed.EffectID)
				versionIDs = append(versionIDs, ed.VersionID)
				workerIDs = append(workerIDs, ed.WorkerID)
				sourceRefs = append(sourceRefs, DigestReference(ed.SourceRef), pathDigest)
			}
		} else {
			stale := ed.CurrentRevision != "" && ed.ReadBasisRevision != "" && ed.ReadBasisRevision != ed.CurrentRevision
			missingBasis := ed.ReadBasisRevision == "" || (len(obs.ReadBases) > 0 && !hasQualifyingRead(sessionID, ed.Path, ed.ReadBasisRevision))
			if stale {
				reasons = append(reasons, "accepted_edit_stale_read_basis")
				effectIDs = append(effectIDs, ed.EffectID)
				versionIDs = append(versionIDs, ed.VersionID)
				workerIDs = append(workerIDs, ed.WorkerID)
				sourceRefs = append(sourceRefs, DigestReference(ed.SourceRef), pathDigest)
			} else if missingBasis {
				reasons = append(reasons, "accepted_edit_missing_qualifying_read_basis")
				effectIDs = append(effectIDs, ed.EffectID)
				versionIDs = append(versionIDs, ed.VersionID)
				workerIDs = append(workerIDs, ed.WorkerID)
				sourceRefs = append(sourceRefs, DigestReference(ed.SourceRef), pathDigest)
			}
		}

		// Check recorded version author against effect author.
		ver, hasVer := versionByID[ed.VersionID]
		if !hasVer && ed.EffectID != "" {
			ver, hasVer = versionByEffect[ed.EffectID]
		}
		if hasVer {
			authorMismatch := ed.AuthorID != "" && ver.AuthorID != "" && ed.AuthorID != ver.AuthorID
			kindMismatch := ed.AuthorKind != "" && ver.AuthorKind != "" && ed.AuthorKind != ver.AuthorKind
			if authorMismatch || kindMismatch {
				if authorMismatch {
					reasons = append(reasons, "version_author_disagrees_with_effect")
				}
				if kindMismatch {
					reasons = append(reasons, "version_author_kind_disagrees_with_effect")
				}
				effectIDs = append(effectIDs, ed.EffectID)
				versionIDs = append(versionIDs, ver.VersionID)
				workerIDs = append(workerIDs, ed.WorkerID, ver.WorkerID)
				sourceRefs = append(sourceRefs, DigestReference(ed.SourceRef), DigestReference(ver.SourceRef), pathDigest)
			}
		}
	}

	if len(reasons) == 0 {
		return Finding{}, false
	}
	slices.Sort(reasons)
	reasons = slices.Compact(reasons)
	f := baseWEAFinding(obs, now, RuleWEAEditBasisOrAuthorMismatch, DispositionContradiction)
	f.ExecutionIDs = effectIDs
	f.OperationIDs = versionIDs
	f.SourceIDs = sourceRefs
	f.Reason = reasons[0]
	f.Expected = map[string]any{
		"qualifying_read_basis_required": true,
		"version_author_matches_effect":  true,
	}
	f.Observed = map[string]any{
		"mismatches":  reasons,
		"effect_ids":  effectIDs,
		"version_ids": versionIDs,
		"worker_ids":  workerIDs,
	}
	return f, true
}

// 3. Effect executed without a matching recorded approval/grant (or grant applied across chats).
func checkWEAUnauthorizedEffect(obs WEAObservation, now time.Time) (Finding, bool) {
	if len(obs.AuthorityEffects) == 0 || obs.RetentionGapped {
		return Finding{}, false
	}
	plansByID := make(map[string]WEAApprovalPlanFact, len(obs.ApprovalPlans))
	for _, p := range obs.ApprovalPlans {
		if p.ApprovalPlanID != "" {
			plansByID[p.ApprovalPlanID] = p
		}
	}
	grantsByID := make(map[string]WEAGrantFact, len(obs.Grants))
	for _, g := range obs.Grants {
		if g.GrantID != "" {
			grantsByID[g.GrantID] = g
		}
	}

	var reasons, effectIDs, planIDs, grantIDs, invocationIDs, sourceRefs []string
	for _, eff := range obs.AuthorityEffects {
		if eff.RetentionPruned || !eff.RequiresAuthority || !eff.Executed || eff.Refused {
			continue
		}
		effSession := eff.SessionID
		if effSession == "" {
			effSession = obs.SessionID
		}

		authorized := false
		var effReasons []string

		if eff.ApprovalPlanID != "" {
			plan, ok := plansByID[eff.ApprovalPlanID]
			if !ok {
				effReasons = append(effReasons, "approval_plan_not_recorded")
			} else if !plan.Approved || plan.Refused {
				effReasons = append(effReasons, "effect_executed_with_unapproved_plan")
			} else if plan.SessionID != "" && effSession != "" && plan.SessionID != effSession {
				effReasons = append(effReasons, "approval_plan_applied_across_sessions")
			} else if plan.ActionDigest != "" && eff.ActionDigest != "" && plan.ActionDigest != eff.ActionDigest {
				effReasons = append(effReasons, "approval_plan_digest_mismatch")
			} else {
				authorized = true
			}
		}

		if !authorized && eff.GrantID != "" {
			grant, ok := grantsByID[eff.GrantID]
			if !ok {
				effReasons = append(effReasons, "grant_not_recorded")
			} else if grant.Scope == "chat" && grant.SessionID != "" && effSession != "" && grant.SessionID != effSession {
				effReasons = append(effReasons, "chat_grant_applied_across_sessions")
			} else if !grant.Active || grant.Revoked || grant.Expired {
				effReasons = append(effReasons, "inactive_or_revoked_grant_used")
			} else if grant.ActionDigest != "" && eff.ActionDigest != "" && grant.ActionDigest != eff.ActionDigest {
				effReasons = append(effReasons, "grant_action_digest_mismatch")
			} else {
				authorized = true
			}
		}

		if !authorized {
			if len(effReasons) == 0 {
				effReasons = append(effReasons, "effect_executed_without_matching_approval_or_grant")
			}
			reasons = append(reasons, effReasons...)
			effectIDs = append(effectIDs, eff.EffectID)
			planIDs = append(planIDs, eff.ApprovalPlanID)
			grantIDs = append(grantIDs, eff.GrantID)
			invocationIDs = append(invocationIDs, eff.InvocationID)
			sourceRefs = append(sourceRefs, DigestReference(eff.SourceRef), DigestReference(eff.TargetRef))
		}
	}

	if len(reasons) == 0 {
		return Finding{}, false
	}
	slices.Sort(reasons)
	reasons = slices.Compact(reasons)
	f := baseWEAFinding(obs, now, RuleWEAUnauthorizedEffect, DispositionContradiction)
	f.ExecutionIDs = effectIDs
	f.OperationIDs = append(append([]string{}, planIDs...), grantIDs...)
	f.SourceIDs = sourceRefs
	f.Reason = reasons[0]
	f.Expected = map[string]any{
		"matching_recorded_approval_or_grant": true,
		"chat_grant_scoped_to_session":        true,
	}
	f.Observed = map[string]any{
		"mismatches":        reasons,
		"effect_ids":        effectIDs,
		"approval_plan_ids": planIDs,
		"grant_ids":         grantIDs,
		"invocation_ids":    invocationIDs,
	}
	return f, true
}

// 4. Observed network egress connection unrecorded by product (or arriving after refusal).
func checkWEAUnrecordedOrRefusedEgress(obs WEAObservation, now time.Time) (Finding, bool) {
	if len(obs.ObservedConnections) == 0 || obs.RetentionGapped {
		return Finding{}, false
	}

	var reasons, connIDs, invocationIDs, decisionIDs, destDigests, sourceRefs []string
	for _, conn := range obs.ObservedConnections {
		if conn.RetentionPruned || (!obs.Settled && !conn.Settled) {
			continue
		}
		connSession := conn.SessionID
		if connSession == "" {
			connSession = obs.SessionID
		}
		connDest := strings.TrimSpace(conn.DestinationRef)
		destDigest := DigestReference(connDest)

		admitted := false
		refused := false
		var matchedDecisionID string

		for _, dec := range obs.EgressDecisions {
			decSession := dec.SessionID
			if decSession == "" {
				decSession = obs.SessionID
			}
			if decSession != "" && connSession != "" && decSession != connSession {
				continue
			}
			if dec.InvocationID != "" && conn.InvocationID != "" && dec.InvocationID != conn.InvocationID {
				continue
			}
			if strings.TrimSpace(dec.DestinationRef) != connDest {
				continue
			}
			if dec.Refused {
				if conn.ObservedOrdinal == 0 || dec.DecisionOrdinal == 0 || conn.ObservedOrdinal >= dec.DecisionOrdinal {
					refused = true
					matchedDecisionID = dec.DecisionID
				}
			} else if dec.Admitted {
				admitted = true
				matchedDecisionID = dec.DecisionID
			}
		}

		if refused {
			reasons = append(reasons, "egress_connection_after_refusal")
			connIDs = append(connIDs, conn.ConnectionID)
			invocationIDs = append(invocationIDs, conn.InvocationID)
			decisionIDs = append(decisionIDs, matchedDecisionID)
			destDigests = append(destDigests, destDigest)
			sourceRefs = append(sourceRefs, DigestReference(conn.SourceRef), destDigest)
		} else if !admitted {
			reasons = append(reasons, "egress_connection_unrecorded_by_product")
			connIDs = append(connIDs, conn.ConnectionID)
			invocationIDs = append(invocationIDs, conn.InvocationID)
			destDigests = append(destDigests, destDigest)
			sourceRefs = append(sourceRefs, DigestReference(conn.SourceRef), destDigest)
		}
	}

	if len(reasons) == 0 {
		return Finding{}, false
	}
	slices.Sort(reasons)
	reasons = slices.Compact(reasons)
	f := baseWEAFinding(obs, now, RuleWEAUnrecordedOrRefusedEgress, DispositionContradiction)
	f.ExecutionIDs = connIDs
	f.OperationIDs = decisionIDs
	f.SourceIDs = sourceRefs
	f.Reason = reasons[0]
	f.Expected = map[string]any{
		"observed_egress_admitted_by_product": true,
		"egress_after_refusal_forbidden":      true,
	}
	f.Observed = map[string]any{
		"mismatches":          reasons,
		"connection_ids":      connIDs,
		"decision_ids":        decisionIDs,
		"invocation_ids":      invocationIDs,
		"destination_digests": destDigests,
	}
	return f, true
}

// 5. Managed secret canary / plaintext appearing in agent-visible or persisted record.
func checkWEASecretCanaryLeak(obs WEAObservation, now time.Time) (Finding, bool) {
	if len(obs.RecordedArtifacts) == 0 {
		return Finding{}, false
	}
	canaryByID := make(map[string]WEASecretCanaryFact, len(obs.SecretCanaries))
	for _, c := range obs.SecretCanaries {
		if c.CanaryID != "" {
			canaryByID[c.CanaryID] = c
		}
	}

	var recordIDs, recordKinds, canaryIDs, canaryDigests, sourceRefs []string
	var workerIDs, jobIDs, resultHandles, effectIDs, invocationIDs []string

	for _, rec := range obs.RecordedArtifacts {
		matched := false
		matchedCanaryID := rec.MatchedCanaryID
		var matchedDigest string

		if rec.CanaryDetected {
			matched = true
			if c, ok := canaryByID[matchedCanaryID]; ok {
				if c.CanaryDigest != "" {
					matchedDigest = c.CanaryDigest
				} else if c.CanaryValue != "" {
					matchedDigest = DigestReference(c.CanaryValue)
				}
			}
		}

		if !matched && rec.ContentBytes != "" {
			for _, c := range obs.SecretCanaries {
				if c.CanaryValue != "" && strings.Contains(rec.ContentBytes, c.CanaryValue) {
					matched = true
					matchedCanaryID = c.CanaryID
					if c.CanaryDigest != "" {
						matchedDigest = c.CanaryDigest
					} else {
						matchedDigest = DigestReference(c.CanaryValue)
					}
					break
				}
			}
		}

		if !matched {
			continue
		}
		recordIDs = append(recordIDs, rec.RecordID)
		recordKinds = append(recordKinds, rec.RecordKind)
		canaryIDs = append(canaryIDs, matchedCanaryID)
		canaryDigests = append(canaryDigests, matchedDigest)
		sourceRefs = append(sourceRefs, DigestReference(rec.SourceRef), DigestReference(rec.RecordKind, rec.RecordID))
		workerIDs = append(workerIDs, rec.WorkerID)
		jobIDs = append(jobIDs, rec.JobID)
		resultHandles = append(resultHandles, rec.ResultHandle)
		effectIDs = append(effectIDs, rec.EffectID)
		invocationIDs = append(invocationIDs, rec.InvocationID)
	}

	if len(recordIDs) == 0 {
		return Finding{}, false
	}
	f := baseWEAFinding(obs, now, RuleWEASecretCanaryLeak, DispositionContradiction)
	f.ExecutionIDs = recordIDs
	f.OperationIDs = canaryIDs
	f.SourceIDs = sourceRefs
	f.Reason = "managed_secret_canary_in_record"
	f.Expected = map[string]any{
		"secret_plaintext_in_records": false,
		"screened_reference_only":     true,
	}
	f.Observed = map[string]any{
		"record_ids":     recordIDs,
		"record_kinds":   recordKinds,
		"canary_ids":     canaryIDs,
		"canary_digests": canaryDigests,
		"worker_ids":     workerIDs,
		"job_ids":        jobIDs,
		"result_handles": resultHandles,
		"effect_ids":     effectIDs,
		"invocation_ids": invocationIDs,
	}
	return f, true
}
