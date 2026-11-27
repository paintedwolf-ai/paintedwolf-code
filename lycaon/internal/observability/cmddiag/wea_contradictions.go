package cmddiag

import (
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

var weaGlobalSeq atomic.Int64

// NextWEASeq returns a strictly monotonic cross-package sequence number for
// ordering independently owned WEA decisions, grants, effects, and connections.
func NextWEASeq() int64 {
	return weaGlobalSeq.Add(1)
}

const weaMaxCorrelatedFacts = 256

// weaCorrelator is a bounded, single-goroutine cache owned by an enabled
// Observer. It joins independently emitted authority plans/grants with settled
// effects, and independently emitted egress decisions with broker/transport
// connection observations, without any package fabricating companion facts.
type weaCorrelator struct {
	plans     map[string]WEAApprovalPlanFact
	planOrder []string

	grants     map[string]WEAGrantFact
	grantOrder []string

	egressByDest map[string][]WEAEgressDecisionFact
	egressOrder  []string
}

func newWEACorrelator() *weaCorrelator {
	return &weaCorrelator{
		plans:        make(map[string]WEAApprovalPlanFact),
		grants:       make(map[string]WEAGrantFact),
		egressByDest: make(map[string][]WEAEgressDecisionFact),
	}
}

func (c *weaCorrelator) correlate(obs WEAObservation) WEAObservation {
	for _, p := range obs.ApprovalPlans {
		if p.ApprovalPlanID == "" {
			continue
		}
		if _, exists := c.plans[p.ApprovalPlanID]; !exists {
			if len(c.planOrder) >= weaMaxCorrelatedFacts {
				delete(c.plans, c.planOrder[0])
				c.planOrder = c.planOrder[1:]
			}
			c.planOrder = append(c.planOrder, p.ApprovalPlanID)
		}
		c.plans[p.ApprovalPlanID] = p
	}
	for _, g := range obs.Grants {
		if g.GrantID == "" {
			continue
		}
		if _, exists := c.grants[g.GrantID]; !exists {
			if len(c.grantOrder) >= weaMaxCorrelatedFacts {
				delete(c.grants, c.grantOrder[0])
				c.grantOrder = c.grantOrder[1:]
			}
			c.grantOrder = append(c.grantOrder, g.GrantID)
		}
		c.grants[g.GrantID] = g
	}
	for _, dec := range obs.EgressDecisions {
		dest := strings.TrimSpace(dec.DestinationRef)
		if dest == "" {
			continue
		}
		list := c.egressByDest[dest]
		if len(list) == 0 {
			if len(c.egressOrder) >= weaMaxCorrelatedFacts {
				delete(c.egressByDest, c.egressOrder[0])
				c.egressOrder = c.egressOrder[1:]
			}
			c.egressOrder = append(c.egressOrder, dest)
		}
		if len(list) >= 16 {
			list = list[1:]
		}
		c.egressByDest[dest] = append(list, dec)
	}

	if len(obs.AuthorityEffects) > 0 {
		havePlan := make(map[string]struct{}, len(obs.ApprovalPlans))
		for _, p := range obs.ApprovalPlans {
			havePlan[p.ApprovalPlanID] = struct{}{}
		}
		haveGrant := make(map[string]struct{}, len(obs.Grants))
		for _, g := range obs.Grants {
			haveGrant[g.GrantID] = struct{}{}
		}
		for _, eff := range obs.AuthorityEffects {
			if eff.ApprovalPlanID != "" {
				if _, ok := havePlan[eff.ApprovalPlanID]; !ok {
					if p, found := c.plans[eff.ApprovalPlanID]; found {
						obs.ApprovalPlans = append(obs.ApprovalPlans, p)
						havePlan[eff.ApprovalPlanID] = struct{}{}
					}
				}
			}
			if eff.GrantID != "" {
				if _, ok := haveGrant[eff.GrantID]; !ok {
					if g, found := c.grants[eff.GrantID]; found {
						obs.Grants = append(obs.Grants, g)
						haveGrant[eff.GrantID] = struct{}{}
					}
				}
			}
		}
	}

	if len(obs.ObservedConnections) > 0 {
		seenDec := make(map[string]struct{}, len(obs.EgressDecisions))
		for _, d := range obs.EgressDecisions {
			if d.DecisionID != "" {
				seenDec[d.DecisionID] = struct{}{}
			}
		}
		for _, conn := range obs.ObservedConnections {
			dest := strings.TrimSpace(conn.DestinationRef)
			for _, d := range c.egressByDest[dest] {
				if d.DecisionID != "" {
					if _, dup := seenDec[d.DecisionID]; dup {
						continue
					}
					seenDec[d.DecisionID] = struct{}{}
				}
				obs.EgressDecisions = append(obs.EgressDecisions, d)
			}
		}
	}
	return obs
}

// Independent RuleIDs for Workers, Edits, and Authority (WEA) ordinary-development
// contradiction diagnostics (TB11).
const (
	RuleWEALandedPathMismatch        RuleID = "wea_landed_path_mismatch"
	RuleWEAEditStaleReadBasis        RuleID = "wea_edit_stale_read_basis"
	RuleWEAEditWrongAuthor           RuleID = "wea_edit_wrong_author"
	RuleWEAUnauthorizedEffect        RuleID = "wea_unauthorized_effect"
	RuleWEAUnrecordedOrRefusedEgress RuleID = "wea_unrecorded_or_refused_egress"
	RuleWEASecretCanaryLeak          RuleID = "wea_secret_canary_leak"
)

// WEAObservation is an immutable snapshot of typed durable facts across the
// Workers, Edits, and Authority tracks, joined strictly by stable identities
// and event ordering (never prose).
type WEAObservation struct {
	SessionID                 string `json:"session_id,omitempty"`
	ProjectID                 string `json:"project_id,omitempty"`
	DeviceID                  string `json:"device_id,omitempty"`
	WorkerID                  string `json:"worker_id,omitempty"`
	JobID                     string `json:"job_id,omitempty"`
	ResultHandle              string `json:"result_handle,omitempty"`
	InvocationID              string `json:"invocation_id,omitempty"`
	ScopeKey                  string `json:"scope_key,omitempty"`
	Settled                   bool   `json:"settled"`
	RetentionGapped           bool   `json:"retention_gapped,omitempty"`
	Truncated                 bool   `json:"truncated,omitempty"`
	ReadWitnessUnavailable    bool   `json:"read_witness_unavailable,omitempty"`
	VersionWitnessUnavailable bool   `json:"version_witness_unavailable,omitempty"`

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
	Incomplete   bool     `json:"incomplete,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
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
	ProjectID      string `json:"project_id,omitempty"`
	InvocationID   string `json:"invocation_id,omitempty"`
	ActionKind     string `json:"action_kind,omitempty"`
	TargetRef      string `json:"target_ref,omitempty"`
	ActionDigest   string `json:"action_digest,omitempty"`
	DecisionSeq    int64  `json:"decision_seq,omitempty"`
	Approved       bool   `json:"approved"`
	Refused        bool   `json:"refused,omitempty"`
	Settled        bool   `json:"settled,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
}

// WEAGrantFact records an installed reusable or exact-action authority grant.
// Scope must be one of "exact_action", "chat", "project", or "device".
type WEAGrantFact struct {
	GrantID      string `json:"grant_id"`
	SessionID    string `json:"session_id,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ActionKind   string `json:"action_kind,omitempty"`
	TargetRef    string `json:"target_ref,omitempty"`
	ActionDigest string `json:"action_digest,omitempty"`
	GrantedSeq   int64  `json:"granted_seq,omitempty"`
	Active       bool   `json:"active"`
	Revoked      bool   `json:"revoked,omitempty"`
	Expired      bool   `json:"expired,omitempty"`
	Settled      bool   `json:"settled,omitempty"`
	SourceRef    string `json:"source_ref,omitempty"`
}

// WEAAuthorityEffectFact records an authority-gated effect execution or refusal.
type WEAAuthorityEffectFact struct {
	EffectID          string `json:"effect_id"`
	SessionID         string `json:"session_id,omitempty"`
	ProjectID         string `json:"project_id,omitempty"`
	DeviceID          string `json:"device_id,omitempty"`
	WorkerID          string `json:"worker_id,omitempty"`
	InvocationID      string `json:"invocation_id,omitempty"`
	ActionKind        string `json:"action_kind,omitempty"`
	TargetRef         string `json:"target_ref,omitempty"`
	ActionDigest      string `json:"action_digest,omitempty"`
	ApprovalPlanID    string `json:"approval_plan_id,omitempty"`
	GrantID           string `json:"grant_id,omitempty"`
	EventSeq          int64  `json:"event_seq,omitempty"`
	RequiresAuthority bool   `json:"requires_authority"`
	Executed          bool   `json:"executed"`
	Refused           bool   `json:"refused,omitempty"`
	RefusalCode       string `json:"refusal_code,omitempty"`
	Settled           bool   `json:"settled,omitempty"`
	RetentionPruned   bool   `json:"retention_pruned,omitempty"`
	SourceRef         string `json:"source_ref,omitempty"`
}

// WEAEgressDecisionFact records a product-side egress gate decision.
// GrantScope governs reuse: empty or "exact_action" binds strictly to
// (SessionID, InvocationID, DestinationRef) with cardinality MaxUses (default 1);
// "chat", "project", and "device" bind to their respective owning identities.
type WEAEgressDecisionFact struct {
	DecisionID     string `json:"decision_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
	DeviceID       string `json:"device_id,omitempty"`
	WorkerID       string `json:"worker_id,omitempty"`
	InvocationID   string `json:"invocation_id,omitempty"`
	DestinationRef string `json:"destination_ref"`
	GrantScope     string `json:"grant_scope,omitempty"`
	MaxUses        int    `json:"max_uses,omitempty"`
	Admitted       bool   `json:"admitted"`
	Refused        bool   `json:"refused,omitempty"`
	RefusalCode    string `json:"refusal_code,omitempty"`
	DecisionSeq    int64  `json:"decision_seq,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
}

// WEAEgressConnectionFact records a host/fixture-observed egress connection.
type WEAEgressConnectionFact struct {
	ConnectionID    string `json:"connection_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	ProjectID       string `json:"project_id,omitempty"`
	DeviceID        string `json:"device_id,omitempty"`
	WorkerID        string `json:"worker_id,omitempty"`
	InvocationID    string `json:"invocation_id,omitempty"`
	DestinationRef  string `json:"destination_ref"`
	ObservedSeq     int64  `json:"observed_seq,omitempty"`
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

// WrapWEAObservation wraps a WEAObservation in a standard cmddiag Observation
// so it flows through the existing Observer lifecycle, queue, deduplication,
// sanitization, and retention pipeline.
func WrapWEAObservation(wea WEAObservation) Observation {
	copyWEA := wea
	return Observation{
		SessionID:  wea.SessionID,
		ProgramID:  weaScopeKey(wea),
		ToolCallID: wea.InvocationID,
		WEA:        &copyWEA,
	}
}

// ObserveWEA submits a WEAObservation to the active cmddiag Observer when
// opt-in diagnostics are enabled. When disabled, it is a zero-allocation no-op.
func ObserveWEA(wea WEAObservation) bool {
	target := activeObserver()
	if target == nil || !target.enabled {
		return false
	}
	return target.Submit(WrapWEAObservation(wea))
}

// EvaluateWEAIfEnabled runs EvaluateWEA only when enabled is true.
// When disabled, it returns nil immediately with zero allocations.
func EvaluateWEAIfEnabled(enabled bool, obs WEAObservation, now time.Time) []Finding {
	if !enabled {
		return nil
	}
	return EvaluateWEA(obs, now)
}

// EvaluateWEA runs all six independently identifiable WEA diagnostic checks
// (worker landing, edit read-basis, edit author, authority approval/grant,
// egress connection, and secret canary leak) against an immutable WEAObservation.
// At most one sanitized finding is emitted per rule per observation, and all
// secret canaries and raw paths/destinations are redacted.
func EvaluateWEA(obs WEAObservation, now time.Time) []Finding {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var out []Finding
	if f, ok := checkWEALandedPathMismatch(obs, now); ok {
		out = append(out, finalizeWEAFinding(f, obs.SecretCanaries))
	}
	if f, ok := checkWEAEditStaleReadBasis(obs, now); ok {
		out = append(out, finalizeWEAFinding(f, obs.SecretCanaries))
	}
	if f, ok := checkWEAEditWrongAuthor(obs, now); ok {
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

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}

// 1. Worker landing: landed path absent from committed worker result (or claimed landed path missing on disk).
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
	var incompleteReasons, incompleteHandles, incompleteSources []string
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
		if res.Incomplete {
			incompleteReasons = append(incompleteReasons, "worker_result_changed_paths_unavailable")
			incompleteHandles = append(incompleteHandles, lp.ResultHandle)
			incompleteSources = append(incompleteSources, DigestReference(res.SourceRef), pathDigest)
			continue
		}
		if _, found := changedSets[lp.ResultHandle][normPath]; !found {
			if res.Truncated || obs.Truncated {
				incompleteReasons = append(incompleteReasons, "truncated_worker_result_paths")
				incompleteHandles = append(incompleteHandles, lp.ResultHandle)
				incompleteSources = append(incompleteSources, DigestReference(res.SourceRef), pathDigest)
				continue
			}
			reasons = append(reasons, "landed_path_absent_from_worker_result")
			handles = append(handles, lp.ResultHandle)
			workerIDs = append(workerIDs, lp.WorkerID, res.WorkerID)
			jobIDs = append(jobIDs, lp.JobID, res.JobID)
			pathDigests = append(pathDigests, pathDigest)
			sourceRefs = append(sourceRefs, DigestReference(lp.SourceRef), DigestReference(res.SourceRef), pathDigest)
		}
	}
	if len(reasons) > 0 {
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
	if len(incompleteReasons) > 0 {
		slices.Sort(incompleteReasons)
		incompleteReasons = slices.Compact(incompleteReasons)
		f := baseWEAFinding(obs, now, RuleWEALandedPathMismatch, DispositionIncomplete)
		f.OperationIDs = incompleteHandles
		f.SourceIDs = incompleteSources
		f.Reason = incompleteReasons[0]
		f.Expected = map[string]any{
			"complete_worker_result_changed_paths": true,
		}
		f.Observed = map[string]any{
			"incomplete_reasons": incompleteReasons,
			"result_handles":     incompleteHandles,
		}
		return f, true
	}
	return Finding{}, false
}

// 2. Edit read-basis: accepted write with stale or missing qualifying read basis.
func checkWEAEditStaleReadBasis(obs WEAObservation, now time.Time) (Finding, bool) {
	if len(obs.EditEffects) == 0 || obs.RetentionGapped {
		return Finding{}, false
	}
	if len(obs.ReadBases) == 0 && obs.ReadWitnessUnavailable {
		var effectIDs, sourceRefs []string
		for _, ed := range obs.EditEffects {
			if ed.RetentionPruned || !ed.Accepted || ed.Refused {
				continue
			}
			effectIDs = append(effectIDs, ed.EffectID)
			sourceRefs = append(sourceRefs, DigestReference(ed.SourceRef), DigestReference(normalizePathKey(ed.Path)))
		}
		if len(effectIDs) == 0 {
			return Finding{}, false
		}
		f := baseWEAFinding(obs, now, RuleWEAEditStaleReadBasis, DispositionIncomplete)
		f.ExecutionIDs = effectIDs
		f.SourceIDs = sourceRefs
		f.Reason = "read_basis_owner_witness_unavailable"
		f.Expected = map[string]any{"read_basis_owner_witness_present": true}
		f.Observed = map[string]any{"read_basis_owner_witness_present": false, "effect_ids": effectIDs}
		return f, true
	}

	hasQualifyingRead := func(sessionID, path, rev string) bool {
		if rev == "" || sessionID == "" {
			return false
		}
		normPath := normalizePathKey(path)
		for _, rb := range obs.ReadBases {
			if rb.RetentionPruned {
				return true
			}
			rbSession := firstNonEmpty(rb.SessionID, obs.SessionID)
			if rbSession == "" || rbSession != sessionID {
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
		sessionID := firstNonEmpty(ed.SessionID, obs.SessionID)
		pathDigest := DigestReference(normalizePathKey(ed.Path))

		if ed.RebasedCleanly {
			basisRev := firstNonEmpty(ed.RebasedFromRevision, ed.ReadBasisRevision)
			if len(obs.ReadBases) > 0 && !hasQualifyingRead(sessionID, ed.Path, basisRev) {
				reasons = append(reasons, "rebased_edit_missing_qualifying_read_basis")
				effectIDs = append(effectIDs, ed.EffectID)
				versionIDs = append(versionIDs, ed.VersionID)
				workerIDs = append(workerIDs, ed.WorkerID)
				sourceRefs = append(sourceRefs, DigestReference(ed.SourceRef), pathDigest)
			}
			continue
		}

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

	if len(reasons) == 0 {
		return Finding{}, false
	}
	slices.Sort(reasons)
	reasons = slices.Compact(reasons)
	f := baseWEAFinding(obs, now, RuleWEAEditStaleReadBasis, DispositionContradiction)
	f.ExecutionIDs = effectIDs
	f.OperationIDs = versionIDs
	f.SourceIDs = sourceRefs
	f.Reason = reasons[0]
	f.Expected = map[string]any{
		"qualifying_read_basis_required": true,
	}
	f.Observed = map[string]any{
		"mismatches":  reasons,
		"effect_ids":  effectIDs,
		"version_ids": versionIDs,
		"worker_ids":  workerIDs,
	}
	return f, true
}

// 3. Edit author: durable file version whose recorded author disagrees with its effect.
func checkWEAEditWrongAuthor(obs WEAObservation, now time.Time) (Finding, bool) {
	if len(obs.EditEffects) == 0 || obs.RetentionGapped {
		return Finding{}, false
	}
	if len(obs.FileVersions) == 0 {
		if !obs.VersionWitnessUnavailable {
			return Finding{}, false
		}
		var effectIDs, sourceRefs []string
		for _, ed := range obs.EditEffects {
			if ed.RetentionPruned || !ed.Accepted || ed.Refused {
				continue
			}
			effectIDs = append(effectIDs, ed.EffectID)
			sourceRefs = append(sourceRefs, DigestReference(ed.SourceRef), DigestReference(normalizePathKey(ed.Path)))
		}
		if len(effectIDs) == 0 {
			return Finding{}, false
		}
		f := baseWEAFinding(obs, now, RuleWEAEditWrongAuthor, DispositionIncomplete)
		f.ExecutionIDs = effectIDs
		f.SourceIDs = sourceRefs
		f.Reason = "version_author_owner_witness_unavailable"
		f.Expected = map[string]any{"version_author_owner_witness_present": true}
		f.Observed = map[string]any{"version_author_owner_witness_present": false, "effect_ids": effectIDs}
		return f, true
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

	var reasons, effectIDs, versionIDs, workerIDs, sourceRefs []string
	for _, ed := range obs.EditEffects {
		if ed.RetentionPruned || !ed.Accepted || ed.Refused {
			continue
		}
		ver, hasVer := versionByID[ed.VersionID]
		if !hasVer && ed.EffectID != "" {
			ver, hasVer = versionByEffect[ed.EffectID]
		}
		if !hasVer {
			continue
		}
		authorMismatch := ed.AuthorID != "" && ver.AuthorID != "" && ed.AuthorID != ver.AuthorID
		kindMismatch := ed.AuthorKind != "" && ver.AuthorKind != "" && ed.AuthorKind != ver.AuthorKind
		if !authorMismatch && !kindMismatch {
			continue
		}
		if authorMismatch {
			reasons = append(reasons, "version_author_disagrees_with_effect")
		}
		if kindMismatch {
			reasons = append(reasons, "version_author_kind_disagrees_with_effect")
		}
		pathDigest := DigestReference(normalizePathKey(firstNonEmpty(ed.Path, ver.Path)))
		effectIDs = append(effectIDs, ed.EffectID)
		versionIDs = append(versionIDs, ver.VersionID)
		workerIDs = append(workerIDs, ed.WorkerID, ver.WorkerID)
		sourceRefs = append(sourceRefs, DigestReference(ed.SourceRef), DigestReference(ver.SourceRef), pathDigest)
	}

	if len(reasons) == 0 {
		return Finding{}, false
	}
	slices.Sort(reasons)
	reasons = slices.Compact(reasons)
	f := baseWEAFinding(obs, now, RuleWEAEditWrongAuthor, DispositionContradiction)
	f.ExecutionIDs = effectIDs
	f.OperationIDs = versionIDs
	f.SourceIDs = sourceRefs
	f.Reason = reasons[0]
	f.Expected = map[string]any{
		"version_author_matches_effect": true,
	}
	f.Observed = map[string]any{
		"mismatches":  reasons,
		"effect_ids":  effectIDs,
		"version_ids": versionIDs,
		"worker_ids":  workerIDs,
	}
	return f, true
}

// 4. Approval/grant authority: effect executed without a matching recorded
// approval plan or scoped grant. Never treats missing IDs or digests as wildcards;
// reports missing required facts as DispositionIncomplete.
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
	var incompleteReasons, incompleteEffects, incompleteSources []string

	for _, eff := range obs.AuthorityEffects {
		if eff.RetentionPruned || !eff.RequiresAuthority || !eff.Executed || eff.Refused {
			continue
		}
		if !obs.Settled && !eff.Settled {
			continue
		}
		effSession := firstNonEmpty(eff.SessionID, obs.SessionID)
		effProject := firstNonEmpty(eff.ProjectID, obs.ProjectID)
		effDevice := firstNonEmpty(eff.DeviceID, obs.DeviceID)
		effInvocation := firstNonEmpty(eff.InvocationID, obs.InvocationID)
		effDigest := strings.TrimSpace(eff.ActionDigest)
		effSrc := DigestReference(eff.SourceRef)
		effTarget := DigestReference(eff.TargetRef)

		if effSession == "" || effDigest == "" {
			incompleteReasons = append(incompleteReasons, "effect_missing_required_session_or_digest")
			incompleteEffects = append(incompleteEffects, eff.EffectID)
			incompleteSources = append(incompleteSources, effSrc, effTarget)
			continue
		}

		authorized := false
		deferred := false
		var effContradictions []string
		var effIncompletes []string

		if eff.ApprovalPlanID != "" {
			plan, ok := plansByID[eff.ApprovalPlanID]
			if !ok {
				effContradictions = append(effContradictions, "approval_plan_not_recorded")
			} else if !obs.Settled && !plan.Settled {
				deferred = true
			} else {
				planSession := strings.TrimSpace(plan.SessionID)
				planDigest := strings.TrimSpace(plan.ActionDigest)
				if planSession == "" || planDigest == "" {
					effIncompletes = append(effIncompletes, "approval_plan_missing_required_session_or_digest")
				} else if !plan.Approved || plan.Refused {
					effContradictions = append(effContradictions, "effect_executed_with_unapproved_plan")
				} else if planSession != effSession {
					effContradictions = append(effContradictions, "approval_plan_applied_across_sessions")
				} else if plan.InvocationID != "" && effInvocation != "" && plan.InvocationID != effInvocation {
					effContradictions = append(effContradictions, "approval_plan_invocation_mismatch")
				} else if planDigest != effDigest {
					effContradictions = append(effContradictions, "approval_plan_digest_mismatch")
				} else if plan.DecisionSeq > 0 && eff.EventSeq > 0 && plan.DecisionSeq > eff.EventSeq {
					effContradictions = append(effContradictions, "approval_plan_recorded_after_effect")
				} else {
					authorized = true
				}
			}
		}

		if !authorized && !deferred && eff.GrantID != "" {
			grant, ok := grantsByID[eff.GrantID]
			if !ok {
				effContradictions = append(effContradictions, "grant_not_recorded")
			} else if !obs.Settled && !grant.Settled {
				deferred = true
			} else {
				grantScope := strings.TrimSpace(grant.Scope)
				grantDigest := strings.TrimSpace(grant.ActionDigest)
				if grantScope == "" || grantDigest == "" {
					effIncompletes = append(effIncompletes, "grant_missing_required_scope_or_digest")
				} else if !grant.Active || grant.Revoked || grant.Expired {
					effContradictions = append(effContradictions, "inactive_or_revoked_grant_used")
				} else if grantDigest != effDigest {
					effContradictions = append(effContradictions, "grant_action_digest_mismatch")
				} else if grant.GrantedSeq > 0 && eff.EventSeq > 0 && grant.GrantedSeq > eff.EventSeq {
					effContradictions = append(effContradictions, "grant_recorded_after_effect")
				} else {
					switch grantScope {
					case "exact_action", "exact":
						grantSession := strings.TrimSpace(grant.SessionID)
						grantInv := strings.TrimSpace(grant.InvocationID)
						if grantSession == "" || grantInv == "" || effInvocation == "" {
							effIncompletes = append(effIncompletes, "exact_action_grant_missing_required_identity")
						} else if grantSession != effSession || grantInv != effInvocation {
							effContradictions = append(effContradictions, "exact_action_grant_scope_mismatch")
						} else {
							authorized = true
						}
					case "chat":
						grantSession := strings.TrimSpace(grant.SessionID)
						if grantSession == "" {
							effIncompletes = append(effIncompletes, "chat_grant_missing_session_id")
						} else if grantSession != effSession {
							effContradictions = append(effContradictions, "chat_grant_applied_across_sessions")
						} else {
							authorized = true
						}
					case "project":
						grantProject := strings.TrimSpace(grant.ProjectID)
						if grantProject == "" || effProject == "" {
							effIncompletes = append(effIncompletes, "project_grant_missing_project_id")
						} else if grantProject != effProject {
							effContradictions = append(effContradictions, "project_grant_applied_across_projects")
						} else {
							authorized = true
						}
					case "device":
						grantDevice := strings.TrimSpace(grant.DeviceID)
						if grantDevice == "" || effDevice == "" {
							effIncompletes = append(effIncompletes, "device_grant_missing_device_id")
						} else if grantDevice != effDevice {
							effContradictions = append(effContradictions, "device_grant_applied_across_devices")
						} else {
							authorized = true
						}
					default:
						effIncompletes = append(effIncompletes, "unknown_grant_scope")
					}
				}
			}
		}

		if authorized || deferred {
			continue
		}
		if len(effContradictions) > 0 {
			reasons = append(reasons, effContradictions...)
			effectIDs = append(effectIDs, eff.EffectID)
			planIDs = append(planIDs, eff.ApprovalPlanID)
			grantIDs = append(grantIDs, eff.GrantID)
			invocationIDs = append(invocationIDs, effInvocation)
			sourceRefs = append(sourceRefs, effSrc, effTarget)
			continue
		}
		if len(effIncompletes) > 0 {
			incompleteReasons = append(incompleteReasons, effIncompletes...)
			incompleteEffects = append(incompleteEffects, eff.EffectID)
			incompleteSources = append(incompleteSources, effSrc, effTarget)
			continue
		}
		reasons = append(reasons, "effect_executed_without_matching_approval_or_grant")
		effectIDs = append(effectIDs, eff.EffectID)
		planIDs = append(planIDs, eff.ApprovalPlanID)
		grantIDs = append(grantIDs, eff.GrantID)
		invocationIDs = append(invocationIDs, effInvocation)
		sourceRefs = append(sourceRefs, effSrc, effTarget)
	}

	if len(reasons) > 0 {
		slices.Sort(reasons)
		reasons = slices.Compact(reasons)
		f := baseWEAFinding(obs, now, RuleWEAUnauthorizedEffect, DispositionContradiction)
		f.ExecutionIDs = effectIDs
		f.OperationIDs = append(append([]string{}, planIDs...), grantIDs...)
		f.SourceIDs = sourceRefs
		f.Reason = reasons[0]
		f.Expected = map[string]any{
			"matching_recorded_approval_or_grant": true,
			"grant_scope_identity_validated":      true,
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
	if len(incompleteReasons) > 0 {
		slices.Sort(incompleteReasons)
		incompleteReasons = slices.Compact(incompleteReasons)
		f := baseWEAFinding(obs, now, RuleWEAUnauthorizedEffect, DispositionIncomplete)
		f.ExecutionIDs = incompleteEffects
		f.SourceIDs = incompleteSources
		f.Reason = incompleteReasons[0]
		f.Expected = map[string]any{
			"required_authority_identities_present": true,
		}
		f.Observed = map[string]any{
			"missing_facts": incompleteReasons,
			"effect_ids":    incompleteEffects,
		}
		return f, true
	}
	return Finding{}, false
}

// 5. Egress connection: observed network egress connection unrecorded by product,
// arriving before admission, reusing an exhausted exact-action decision, or
// arriving under a refused action (without a valid superseding later admission).
// Never treats missing invocation/session/sequence fields as wildcards.
func checkWEAUnrecordedOrRefusedEgress(obs WEAObservation, now time.Time) (Finding, bool) {
	if len(obs.ObservedConnections) == 0 || obs.RetentionGapped {
		return Finding{}, false
	}

	// Order connections deterministically by ObservedSeq so exact-action
	// cardinality tracking is reproducible.
	conns := make([]WEAEgressConnectionFact, len(obs.ObservedConnections))
	copy(conns, obs.ObservedConnections)
	slices.SortStableFunc(conns, func(a, b WEAEgressConnectionFact) int {
		if a.ObservedSeq < b.ObservedSeq {
			return -1
		}
		if a.ObservedSeq > b.ObservedSeq {
			return 1
		}
		return strings.Compare(a.ConnectionID, b.ConnectionID)
	})

	exactUses := make(map[string]int, len(obs.EgressDecisions))

	var reasons, connIDs, invocationIDs, decisionIDs, destDigests, sourceRefs []string
	var incompleteReasons, incompleteConns, incompleteSources []string

	for _, conn := range conns {
		if conn.RetentionPruned || (!obs.Settled && !conn.Settled) {
			continue
		}
		connSession := firstNonEmpty(conn.SessionID, obs.SessionID)
		connProject := firstNonEmpty(conn.ProjectID, obs.ProjectID)
		connDevice := firstNonEmpty(conn.DeviceID, obs.DeviceID)
		connInv := strings.TrimSpace(conn.InvocationID)
		connDest := strings.TrimSpace(conn.DestinationRef)
		destDigest := DigestReference(connDest)
		connSrc := DigestReference(conn.SourceRef)

		if connSession == "" || connInv == "" || connDest == "" || conn.ObservedSeq <= 0 {
			incompleteReasons = append(incompleteReasons, "egress_connection_missing_required_identity_or_seq")
			incompleteConns = append(incompleteConns, conn.ConnectionID)
			incompleteSources = append(incompleteSources, connSrc, destDigest)
			continue
		}

		var latestRefusalSeq int64
		var latestRefusalID string
		var bestAdmitIdx = -1
		var bestAdmitSeq int64
		var exhaustedExactID string
		var postArrivalAdmitID string
		var sawIncompleteDecision bool

		for i, dec := range obs.EgressDecisions {
			if strings.TrimSpace(dec.DestinationRef) != connDest {
				continue
			}
			if dec.DecisionSeq <= 0 {
				sawIncompleteDecision = true
				continue
			}
			decSession := strings.TrimSpace(dec.SessionID)
			decInv := strings.TrimSpace(dec.InvocationID)

			if dec.Refused {
				// Refusals bind strictly to the owning action (session + invocation + destination).
				if decSession == "" || decInv == "" {
					sawIncompleteDecision = true
					continue
				}
				if decSession == connSession && decInv == connInv && dec.DecisionSeq <= conn.ObservedSeq {
					if dec.DecisionSeq >= latestRefusalSeq {
						latestRefusalSeq = dec.DecisionSeq
						latestRefusalID = dec.DecisionID
					}
				}
				continue
			}

			if !dec.Admitted {
				continue
			}

			scope := strings.TrimSpace(dec.GrantScope)
			if scope == "" {
				scope = "exact_action"
			}

			matchesScope := false
			switch scope {
			case "exact_action", "exact":
				if decSession == "" || decInv == "" {
					sawIncompleteDecision = true
					continue
				}
				matchesScope = decSession == connSession && decInv == connInv
			case "chat":
				if decSession == "" {
					sawIncompleteDecision = true
					continue
				}
				matchesScope = decSession == connSession
			case "project":
				if strings.TrimSpace(dec.ProjectID) == "" || connProject == "" {
					sawIncompleteDecision = true
					continue
				}
				matchesScope = strings.TrimSpace(dec.ProjectID) == connProject
			case "device":
				if strings.TrimSpace(dec.DeviceID) == "" || connDevice == "" {
					sawIncompleteDecision = true
					continue
				}
				matchesScope = strings.TrimSpace(dec.DeviceID) == connDevice
			default:
				sawIncompleteDecision = true
				continue
			}

			if !matchesScope {
				continue
			}

			// Admitting decision must precede the observed arrival.
			if dec.DecisionSeq > conn.ObservedSeq {
				if postArrivalAdmitID == "" {
					postArrivalAdmitID = dec.DecisionID
				}
				continue
			}

			// Enforce cardinality for exact-action decisions.
			if scope == "exact_action" || scope == "exact" {
				key := firstNonEmpty(dec.DecisionID, decSession+"|"+decInv+"|"+connDest)
				maxUses := dec.MaxUses
				if maxUses <= 0 {
					maxUses = 1
				}
				if exactUses[key] >= maxUses {
					if exhaustedExactID == "" {
						exhaustedExactID = dec.DecisionID
					}
					continue
				}
			}

			if dec.DecisionSeq >= bestAdmitSeq {
				bestAdmitSeq = dec.DecisionSeq
				bestAdmitIdx = i
			}
		}

		// A valid later admission superseding an earlier refusal (bestAdmitSeq > latestRefusalSeq)
		// authorizes the connection cleanly.
		if bestAdmitIdx >= 0 && bestAdmitSeq > latestRefusalSeq {
			dec := obs.EgressDecisions[bestAdmitIdx]
			scope := strings.TrimSpace(dec.GrantScope)
			if scope == "" || scope == "exact_action" || scope == "exact" {
				key := firstNonEmpty(dec.DecisionID, strings.TrimSpace(dec.SessionID)+"|"+strings.TrimSpace(dec.InvocationID)+"|"+connDest)
				exactUses[key]++
			}
			continue
		}

		if latestRefusalSeq > 0 {
			reasons = append(reasons, "egress_connection_after_refusal")
			connIDs = append(connIDs, conn.ConnectionID)
			invocationIDs = append(invocationIDs, connInv)
			decisionIDs = append(decisionIDs, latestRefusalID)
			destDigests = append(destDigests, destDigest)
			sourceRefs = append(sourceRefs, connSrc, destDigest)
			continue
		}
		if exhaustedExactID != "" {
			reasons = append(reasons, "egress_exact_decision_reused")
			connIDs = append(connIDs, conn.ConnectionID)
			invocationIDs = append(invocationIDs, connInv)
			decisionIDs = append(decisionIDs, exhaustedExactID)
			destDigests = append(destDigests, destDigest)
			sourceRefs = append(sourceRefs, connSrc, destDigest)
			continue
		}
		if postArrivalAdmitID != "" {
			reasons = append(reasons, "egress_connection_before_admission")
			connIDs = append(connIDs, conn.ConnectionID)
			invocationIDs = append(invocationIDs, connInv)
			decisionIDs = append(decisionIDs, postArrivalAdmitID)
			destDigests = append(destDigests, destDigest)
			sourceRefs = append(sourceRefs, connSrc, destDigest)
			continue
		}
		if sawIncompleteDecision {
			incompleteReasons = append(incompleteReasons, "egress_decision_missing_required_identity_or_seq")
			incompleteConns = append(incompleteConns, conn.ConnectionID)
			incompleteSources = append(incompleteSources, connSrc, destDigest)
			continue
		}

		reasons = append(reasons, "egress_connection_unrecorded_by_product")
		connIDs = append(connIDs, conn.ConnectionID)
		invocationIDs = append(invocationIDs, connInv)
		destDigests = append(destDigests, destDigest)
		sourceRefs = append(sourceRefs, connSrc, destDigest)
	}

	if len(reasons) > 0 {
		slices.Sort(reasons)
		reasons = slices.Compact(reasons)
		f := baseWEAFinding(obs, now, RuleWEAUnrecordedOrRefusedEgress, DispositionContradiction)
		f.ExecutionIDs = connIDs
		f.OperationIDs = decisionIDs
		f.SourceIDs = sourceRefs
		f.Reason = reasons[0]
		f.Expected = map[string]any{
			"observed_egress_preceded_by_admitting_decision": true,
			"egress_after_refusal_forbidden":                 true,
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
	if len(incompleteReasons) > 0 {
		slices.Sort(incompleteReasons)
		incompleteReasons = slices.Compact(incompleteReasons)
		f := baseWEAFinding(obs, now, RuleWEAUnrecordedOrRefusedEgress, DispositionIncomplete)
		f.ExecutionIDs = incompleteConns
		f.SourceIDs = incompleteSources
		f.Reason = incompleteReasons[0]
		f.Expected = map[string]any{
			"required_egress_identities_and_seq_present": true,
		}
		f.Observed = map[string]any{
			"missing_facts":  incompleteReasons,
			"connection_ids": incompleteConns,
		}
		return f, true
	}
	return Finding{}, false
}

// 6. Managed secret canary / plaintext appearing in agent-visible or persisted record.
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
