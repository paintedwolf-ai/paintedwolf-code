// Package report defines the composed ReportInput contract for report PDFs.
// Assembly builds ReportInput from durable records; the renderer consumes it.
// The workflow authorizes generation before assembling the input.
package report

import (
	"strings"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
)

// ReportInput is the host-managed shape rendered into a PDF.
//
// The first page is the brief, the second the working summary; later sections
// are the record behind them. Optional sections follow the durable records.
type ReportInput struct {
	// CoverageReview is present only for workflows declaring reviewed coverage.
	CoverageReview *reviewcoverage.Review `json:"coverage_review,omitempty"`
	CoverageFacts  *reviewcoverage.Facts  `json:"coverage_facts,omitempty"`

	// Title names the workflow document.
	Title string `json:"title"`
	// Headline is the closeout's one-sentence conclusion. It opens the working
	// summary; the brief leads with the host's rating instead.
	Headline string `json:"headline,omitempty"`
	// Summary is the plain-language assessment, a short paragraph, written for
	// a reader who will not open the detail behind it.
	Summary string `json:"summary,omitempty"`
	RunID   string `json:"run_id"`
	Project string `json:"project"`
	// StartedAt and CompletedAt bound the work, RFC 3339.
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at"`
	HeadSHA     string `json:"head_sha"`
	// Workflow identifies the declared deliverable's workflow at run scope.
	Workflow *ReportWorkflow `json:"workflow,omitempty"`
	// Workforce is what produced the work: the model, and the worker legs
	// that ran, by agent type.
	Workforce *ReportWorkforce `json:"workforce,omitempty"`
	// Findings are the assessed conclusions a reader acts on, most severe
	// first. They are the report's subject, not the scanner's output.
	Findings []ReportFinding `json:"findings,omitempty"`
	// FindingsLabel is what the subject calls them — findings, options,
	// defects, review points. Vocabulary only: it names the section and its
	// column, and changes no structure. Empty takes the renderer's own word.
	FindingsLabel string `json:"findings_label,omitempty"`
	// Limits are the areas the closeout declared it did not examine.
	Limits []string `json:"limits,omitempty"`
	// Gaps are the host's own account of unfinished work, by kind.
	Gaps     []ReportGap          `json:"gaps,omitempty"`
	Coverage []ReportCoverageItem `json:"coverage,omitempty"`
	// Brief is the rating the workflow declared, decided from the rated
	// findings' answers. Nil when the workflow rates nothing.
	Brief *ReportBrief `json:"brief,omitempty"`
	// Ask is the one decision the report asks of its reader.
	Ask *ReportAsk `json:"ask,omitempty"`
	// UnreportedClaims counts the claims a review left open or overturned that
	// no finding carries: conclusions the report owes and did not state.
	UnreportedClaims int `json:"unreported_claims,omitempty"`
	// Defects are the document requirements the stored report failed. Any
	// defect means the report was not accepted and its run failed.
	Defects []ReportDefect `json:"defects,omitempty"`
	// Checks are what the work covered, one row per planned area, review, and
	// scan inventory.
	Checks []ReportCheck `json:"checks,omitempty"`
	// Claims are the run's claims as its review phases left them.
	Claims []ReportClaim `json:"claims,omitempty"`
	// Inventory accounts for the scanner groups the run's review was given.
	Inventory *ReportInventory `json:"inventory,omitempty"`
	// Synthesis is the narrative behind the findings.
	Synthesis string `json:"synthesis"`
	// Verdicts are every review verdict the subject recorded, in phase order.
	Verdicts []ReportVerdict `json:"verdicts,omitempty"`
	Scan     *ReportScan     `json:"scan,omitempty"`
	// ScanRules are the rules ScanRows reference, so a rule's description
	// prints once rather than under every location.
	ScanRules []ReportScanRule `json:"scan_rules,omitempty"`
	// ScanRows are the scanner rows the report lists — a bounded selection of
	// what the scan stored, with Scan carrying the counts behind it.
	ScanRows []ReportScanRow  `json:"scan_rows,omitempty"`
	Evidence []ReportEvidence `json:"evidence,omitempty"`
	// EvidenceTotal is the ledger size Evidence was drawn from. Larger than
	// len(Evidence) when the wire capped the sample.
	EvidenceTotal int `json:"evidence_total,omitempty"`
	// Sources are the web pages the subject cited.
	Sources []ReportSource `json:"sources,omitempty"`
	// Artifacts are the durable visuals the subject produced. Captures
	// (non-empty evidence_handle) land in the evidence appendix; renders
	// (no handle) in a featured Visuals section. Bytes are host-resolved
	// at assembly time; fixtures may omit bytes or supply base64.
	Artifacts []ReportArtifact `json:"artifacts,omitempty"`
}

// ReportWorkflow names the workflow a run-scoped report delivers.
type ReportWorkflow struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}

// ReportCoverageItem is an observed workflow fact, separate from assessment prose.
type ReportCoverageItem struct {
	Subject string `json:"subject"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
}

// ReportWorkforce is what produced the subject's work.
type ReportWorkforce struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Agents counts the worker legs that ran, by agent type, in the order
	// they first appeared.
	Agents []ReportAgentCount `json:"agents,omitempty"`
}

// ReportAgentCount is one agent type and how many legs of it ran.
type ReportAgentCount struct {
	Type string `json:"type"`
	Legs int    `json:"legs"`
}

// ReportFinding is one assessed conclusion. A finding that needs no action is
// still one: an examined surface that held is stated, not omitted.
type ReportFinding struct {
	// ID is a stable reference a reader can quote, and the key a verdict
	// claim shares when the finding was adjudicated.
	ID string `json:"id,omitempty"`
	// Title is the finding in one line, in plain language.
	Title string `json:"title"`
	// Severity is ranked like a scan level; an unranked word renders plainly.
	Severity string `json:"severity,omitempty"`
	// SeverityTone is the declared tone of the rating level Severity names,
	// set when the workflow's rating decided it; it outranks the word's rank.
	SeverityTone string `json:"severity_tone,omitempty"`
	// Status is the subject's own word for where the finding stands.
	Status string `json:"status,omitempty"`
	// Impact is what it means if nothing is done.
	Impact string `json:"impact,omitempty"`
	// Action is what to do about it.
	Action string `json:"action,omitempty"`
	// Where locates the finding, by place or by evidence handle.
	Where []ReportClaimCitation `json:"where,omitempty"`
	// Disposition is act, accept, or held.
	Disposition string `json:"disposition,omitempty"`
}

// Finding dispositions.
const (
	DispositionAct    = "act"
	DispositionAccept = "accept"
	DispositionHeld   = "held"
)

// NeedsAttention reports whether a finding is work to do or a risk kept on
// purpose. A finding that states no disposition is treated as work to do.
func (f ReportFinding) NeedsAttention() bool {
	return f.Disposition != DispositionHeld
}

// ReportBrief is the declared rating, decided.
type ReportBrief struct {
	Question string `json:"question"`
	// Levels run most severe first.
	Levels []ReportLevel `json:"levels"`
	// Worst and Best index Levels; they differ when an unknown answer could
	// decide either.
	Worst int `json:"worst"`
	Best  int `json:"best"`
	// Basis says why the deciding finding set Worst, in declared phrases.
	Basis string `json:"basis,omitempty"`
	// Dimensions label the rating questions, in declared order.
	Dimensions []string      `json:"dimensions,omitempty"`
	Rated      []ReportRated `json:"rated,omitempty"`
}

// ReportLevel is one step of a declared scale.
type ReportLevel struct {
	Label  string `json:"label"`
	Answer string `json:"answer,omitempty"`
	Means  string `json:"means"`
	Tone   string `json:"tone,omitempty"`
}

// ReportRated is one finding the rating was decided from.
type ReportRated struct {
	// Number is the finding's position in Findings, from one.
	Number int    `json:"number"`
	Title  string `json:"title"`
	// Answers are value labels, index-aligned with the brief's Dimensions.
	Answers []string `json:"answers"`
	// Worst and Best index the brief's Levels.
	Worst int `json:"worst"`
	Best  int `json:"best"`
	// Adjudicated marks answers a review phase stated for a claim with the
	// finding's id, rather than the closeout alone.
	Adjudicated bool `json:"adjudicated,omitempty"`
	// Unreported marks a claim the review left open or overturned that no
	// finding carries; it has no number in the findings.
	Unreported bool `json:"unreported,omitempty"`
}

// ReportAsk is the one decision the report asks of its reader.
type ReportAsk struct {
	Do     string `json:"do"`
	Effort string `json:"effort"`
	Why    string `json:"why,omitempty"`
}

// Codes of the document requirements a report can fail.
const (
	DefectFenceUnreadable      = "REPORT_FENCE_UNREADABLE"
	DefectDocumentInvalid      = "REPORT_DOCUMENT_INVALID"
	DefectClaimUnreported      = "REPORT_CLAIM_UNREPORTED"
	DefectInventoryUnaccounted = "REPORT_INVENTORY_UNACCOUNTED"
)

// ReportDefect is one document requirement the stored report failed.
type ReportDefect struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
	// Subjects sample what the reason is about; Count is how many there are.
	Subjects []string `json:"subjects,omitempty"`
	Count    int      `json:"count,omitempty"`
}

// Kinds of host gap.
const (
	GapInventoryUnaccounted = "inventory_unaccounted"
	GapClaimsOpen           = "claims_open"
	GapLegsUnfinished       = "legs_unfinished"
	GapLegsPartial          = "legs_partial"
	GapWorkersPartial       = "workers_partial"
	GapScansFailed          = "scans_failed"
	GapScansMoved           = "scans_moved"
	GapScansStanding        = "scans_standing"
)

// ReportGap is one kind of unfinished work, counted against its whole.
type ReportGap struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Of    int    `json:"of,omitempty"`
	// Names say which: area subjects, scanner ids, claim titles.
	Names []string `json:"names,omitempty"`
	// Detail counts what inside the named things is affected: moved files,
	// or engine limits and the files they touch.
	Detail       int  `json:"detail,omitempty"`
	DetailFiles  int  `json:"detail_files,omitempty"`
	UnknownScope bool `json:"unknown_scope,omitempty"`
}

// Completeness levels.
const (
	CompletenessComplete   = "complete"
	CompletenessMostly     = "mostly"
	CompletenessIncomplete = "incomplete"
)

// Completeness retains hard failures, then applies the accepted coverage review.
// Workflows without a declared review use the observed gap classification.
func (in ReportInput) Completeness() string {
	if len(in.Defects) > 0 || in.UnreportedClaims > 0 || (in.Inventory != nil && in.Inventory.Unaccounted > 0) {
		return CompletenessIncomplete
	}
	level := CompletenessComplete
	for _, g := range in.Gaps {
		if g.Count == 0 {
			continue
		}
		switch g.Kind {
		case GapInventoryUnaccounted, GapClaimsOpen, GapLegsUnfinished, GapScansFailed:
			return CompletenessIncomplete
		case GapLegsPartial, GapWorkersPartial, GapScansMoved:
			level = CompletenessMostly
		}
	}
	if in.CoverageFacts != nil {
		if in.CoverageReview == nil || reviewcoverage.Validate(*in.CoverageFacts, *in.CoverageReview) != nil {
			return CompletenessIncomplete
		}
		return in.CoverageReview.Completeness()
	}
	return level
}

// Check kinds and states.
const (
	CheckArea   = "area"
	CheckReview = "review"
	CheckScans  = "scans"

	CheckDone      = "checked"
	CheckPartial   = "partial"
	CheckUnchecked = "unchecked"
)

// ReportCheck is one thing the work covered. The renderer words it by kind.
type ReportCheck struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	State   string `json:"state"`
	// Held, Failed, and Open count a review's claims.
	Held   int `json:"held,omitempty"`
	Failed int `json:"failed,omitempty"`
	Open   int `json:"open,omitempty"`
	// Ran and ScansFailed count scans; Used and Total, scanner groups.
	Ran         int `json:"ran,omitempty"`
	ScansFailed int `json:"scans_failed,omitempty"`
	Used        int `json:"used,omitempty"`
	Total       int `json:"total,omitempty"`
}

// Claim classes.
const (
	ClaimHeld   = "held"
	ClaimFailed = "failed"
	ClaimOpen   = "open"
)

// ReportClaim is one claim as the run's review phases left it.
type ReportClaim struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Class  string `json:"class"`
	Status string `json:"status,omitempty"`
	// Dropped marks a claim a later review phase did not restate.
	Dropped bool `json:"dropped,omitempty"`
}

// ReportInventory accounts for the scanner groups a run's review was given.
type ReportInventory struct {
	Total       int              `json:"total"`
	Linked      int              `json:"linked"`
	SetAside    int              `json:"set_aside"`
	Unaccounted int              `json:"unaccounted"`
	SetAsides   []ReportSetAside `json:"set_asides,omitempty"`
}

// ReportSetAside is one reason scanner groups were accounted for together.
type ReportSetAside struct {
	Reason string `json:"reason"`
	Groups int    `json:"groups"`
}

// ReportArtifact is one visual for the PDF (metadata contract + resolved bytes).
// EvidenceHandle decides where it lands: a handle makes it a grounded capture
// for the appendix, no handle a featured render.
type ReportArtifact struct {
	ID             string `json:"id"`
	Caption        string `json:"caption"`
	EvidenceHandle string `json:"evidence_handle,omitempty"`
	// Mime and Bytes are filled by assembly from the durable overlay; omitted
	// when the blob is missing/evicted (section shrinks — never a broken image).
	Mime  string `json:"mime,omitempty"`
	Bytes []byte `json:"bytes,omitempty"`
}

// ReportVerdict is one review verdict a phase recorded. A phase declares its
// own `verdict_schema`, so members arrive as ordered name/value pairs rather
// than as a fixed field list.
type ReportVerdict struct {
	ReconcilesPhase string `json:"reconciles_phase,omitempty"`
	// Phase is the manifest phase id that stamped the verdict; Label its
	// activity label, for a reader.
	Phase string `json:"phase,omitempty"`
	Label string `json:"label,omitempty"`
	// Decision is the terminal verdict word from the reserved `verdict` member.
	Decision string `json:"decision"`
	// RecordedAt is when the record was stamped, RFC 3339.
	RecordedAt string `json:"recorded_at,omitempty"`
	// Fields are the schema's other members, in the order assembly recorded
	// them (decision first, then the rest alphabetically). A claims-typed
	// member is not repeated here, and the reserved citation channels never
	// appear.
	Fields []ReportVerdictField `json:"fields,omitempty"`
	// Claims are the adjudicated statements from every claims-typed member,
	// each tracing to its own evidence.
	Claims []ReportVerdictClaim `json:"claims,omitempty"`
}

// ReportVerdictField is one non-claims member of a stamped verdict. Name is the
// raw schema key; the renderer decides how it is written for a reader.
type ReportVerdictField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ReportVerdictClaim is one adjudicated statement and the evidence backing it.
// A later phase that restates an earlier claim's id adjudicates it; Status is
// that phase's word for the outcome, in the workflow's own vocabulary.
type ReportVerdictClaim struct {
	ScanGroupIDs  []string              `json:"scan_group_ids,omitempty"`
	ID            string                `json:"id"`
	Title         string                `json:"title,omitempty"`
	Statement     string                `json:"statement"`
	Status        string                `json:"status,omitempty"`
	CitedEvidence []ReportClaimCitation `json:"cited_evidence,omitempty"`
}

// ReportClaimCitation locates one piece of evidence a claim rests on: an
// evidence handle, a place, or both.
type ReportClaimCitation struct {
	Handle string `json:"handle,omitempty"`
	Path   string `json:"path,omitempty"`
	Line   int    `json:"line,omitempty"`
}

// ReportScan is the scan section's coverage. A reported row is merged, withheld
// at ingest, or stored; a stored row is listed or held back by the listing
// floor. Ignored rows are a subset of stored.
type ReportScan struct {
	Executions   []ReportScanExecution `json:"executions,omitempty"`
	Groups       int                   `json:"groups,omitempty"`
	ListedGroups int                   `json:"listed_groups,omitempty"`
	Represented  int                   `json:"represented,omitempty"`
	// Scanners are the scanner ids or categories that produced the findings.
	Scanners []string `json:"scanners,omitempty"`
	// Total is every finding the scanners reported.
	Total int `json:"total"`
	// Stored is how many the scan kept after merging and its ingest budget.
	Stored int `json:"stored,omitempty"`
	// Listed is how many rows the report carries.
	Listed int `json:"listed"`
	// ByLevel counts findings per severity label, including levels at zero.
	ByLevel map[string]int `json:"by_level,omitempty"`
	// Ignored counts stored findings an ignore entry covers.
	Ignored int `json:"ignored,omitempty"`
	// Merged counts findings folded into another row at ingest.
	Merged int `json:"merged,omitempty"`
}

// WithheldAtIngest is what the scanners reported that the scan did not keep,
// beyond the rows it merged.
func (s ReportScan) WithheldAtIngest() int {
	return positive(s.Total - s.Merged - s.stored())
}

// NotListed is what the scan stored that the report did not list.
func (s ReportScan) NotListed() int {
	if s.Groups > 0 {
		return positive(s.stored() - s.Represented)
	}
	return positive(s.stored() - s.Listed)
}

// ReportScanExecution includes empty successes and unsuccessful scan attempts.
type ReportScanExecution struct {
	Coverage string `json:"coverage,omitempty"`
	ID       string `json:"id"`
	Scanner  string `json:"scanner"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// stored falls back to the listed rows when assembly recorded no store count,
// so a scan that carried everything reports nothing withheld.
func (s ReportScan) stored() int {
	if s.Stored > 0 {
		return s.Stored
	}
	return s.Listed
}

func positive(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// ReportScanRule is one scanner rule the listed rows reference.
type ReportScanRule struct {
	Details       []string `json:"details,omitempty"`
	GroupID       string   `json:"group_id,omitempty"`
	Package       string   `json:"package,omitempty"`
	Occurrences   int      `json:"occurrences,omitempty"`
	LocationCount int      `json:"location_count,omitempty"`
	ID            string   `json:"id"`
	Scanner       string   `json:"scanner,omitempty"`
	// Description is the rule's message when every row under it carries the
	// same one; empty when they differ, in which case each row prints its own.
	Description string `json:"description,omitempty"`
}

// DisplayID is the rule id without the scanner's own prefix, which the
// scanner column already states.
func (r ReportScanRule) DisplayID() string {
	id := strings.TrimSpace(r.ID)
	if scanner := strings.TrimSpace(r.Scanner); scanner != "" {
		id = strings.TrimPrefix(id, scanner+":")
	}
	return id
}

// ReportScanRow is one scanner row the report lists.
type ReportScanRow struct {
	GroupID  string `json:"group_id,omitempty"`
	Severity string `json:"severity"`
	RuleID   string `json:"rule_id"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
}

// ReportEvidence is one grounded evidence row for the appendix. Which facts a
// row carries depends on what its tool observed: a search has matches, a read
// has a line span and an excerpt, a fetch has a URL.
type ReportEvidence struct {
	Handle string `json:"handle"`
	// Scope is the leg or session the handle belongs to. The appendix names
	// it as a column only when the records span more than one.
	Scope     string `json:"scope,omitempty"`
	Kind      string `json:"kind"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
	URL       string `json:"url,omitempty"`
	Excerpt   string `json:"excerpt,omitempty"`
	TrustTier string `json:"trust_tier"`
	// Matches and MatchFiles are a search record's matching lines and the
	// distinct files they fell in.
	Matches    int  `json:"matches,omitempty"`
	MatchFiles int  `json:"match_files,omitempty"`
	Truncated  bool `json:"truncated,omitempty"`
	// CitedBy names what rests on this record: "report" for the closeout, a
	// claim id, or a phase id. Empty for a record nothing cited.
	CitedBy []string `json:"cited_by,omitempty"`
}

// Cited reports whether anything in the report rests on this record.
func (e ReportEvidence) Cited() bool { return len(e.CitedBy) > 0 }

// ReportSource is one web page the subject cited.
type ReportSource struct {
	URL string `json:"url"`
	// Title is the page title a tool observed; empty when no tool saw one.
	Title string `json:"title,omitempty"`
	// CitedBy names what cited the page: "report" or a phase id.
	CitedBy []string `json:"cited_by,omitempty"`
}
