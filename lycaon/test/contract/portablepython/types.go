package portablepython

const (
	OracleSchemaVersion = "portable-python-oracle/1"
	GraderVersion       = "py06-grader/1"
)

// Guarantee names an independently graded Portable Python contract property.
type Guarantee string

const (
	GuaranteeNativeRoute                Guarantee = "native_route"
	GuaranteeDocumentView               Guarantee = "document_view"
	GuaranteeExactlyOncePublication     Guarantee = "exactly_once_publication"
	GuaranteeGrounding                  Guarantee = "grounding"
	GuaranteePythonOperationAttribution Guarantee = "python_operation_attribution"
	GuaranteeFileContent                Guarantee = "file_content"
	GuaranteeDiscovery                  Guarantee = "discovery"
	GuaranteePythonCompatibility        Guarantee = "python_compatibility"
	GuaranteeAuthority                  Guarantee = "authority"
	GuaranteeProvenance                 Guarantee = "provenance"
	GuaranteeFinalArtifact              Guarantee = "final_artifact"
)

// VerdictStatus records the outcome for one guarantee.
type VerdictStatus string

const (
	VerdictPass          VerdictStatus = "pass"
	VerdictFail          VerdictStatus = "fail"
	VerdictNotApplicable VerdictStatus = "not_applicable"
)

// GuaranteeVerdict captures an independent pass/fail decision for one guarantee.
type GuaranteeVerdict struct {
	Guarantee Guarantee     `json:"guarantee"`
	Status    VerdictStatus `json:"status"`
	Reason    string        `json:"reason,omitempty"`
	Evidence  string        `json:"evidence,omitempty"`
}

// PackageExpectation describes launch-package and CLI expectations for a case.
type PackageExpectation struct {
	RequireInventoryMatch  bool     `json:"require_inventory_match,omitempty"`
	ForbiddenTempPatterns  []string `json:"forbidden_temp_patterns,omitempty"`
	ExpectedCLI            string   `json:"expected_cli,omitempty"`
	ExpectMissingPluginErr bool     `json:"expect_missing_plugin_err,omitempty"`
}

// CaseSpec defines the versioned inputs and independent oracle expectations for a qualification case.
type CaseSpec struct {
	ID                 string             `json:"id"`
	Version            string             `json:"version"`
	OracleVersion      string             `json:"oracle_version"`
	Actor              string             `json:"actor"`
	Authority          string             `json:"authority"`
	Program            string             `json:"program"`
	ExpectedRoute      string             `json:"expected_route"`
	ExpectedSentinels  map[string]string  `json:"expected_sentinels,omitempty"`
	ExpectedFiles      map[string][]byte  `json:"expected_files,omitempty"`
	ExpectedOutcome    string             `json:"expected_outcome,omitempty"`
	PackageExpectation PackageExpectation `json:"package_expectation,omitempty"`
}

// RouteRecord captures host execution routing facts observed outside the guest.
type RouteRecord struct {
	Kind             string `json:"kind"`
	ExternalFallback bool   `json:"external_fallback,omitempty"`
	SystemBinaryPath string `json:"system_binary_path,omitempty"`
}

// PublicationReceipt records one host publication or settlement event.
type PublicationReceipt struct {
	OperationID       string `json:"operation_id"`
	TargetPath        string `json:"target_path"`
	CommitCount       int    `json:"commit_count"`
	Witnessed         bool   `json:"witnessed"`
	SourceConfidence  string `json:"source_confidence,omitempty"`
	PublishedRevision int    `json:"published_revision,omitempty"`
}

// CitationRecord captures a model-visible source citation emitted during a run.
type CitationRecord struct {
	Path                 string `json:"path"`
	FromGuestRead        bool   `json:"from_guest_read"`
	DerivedOnly          bool   `json:"derived_only"`
	NativeRangeDelivered bool   `json:"native_range_delivered"`
}

// ActivityRecord captures how the host attributed a workspace mutation.
type ActivityRecord struct {
	TargetPath    string `json:"target_path"`
	OperationKind string `json:"operation_kind"`
	Interpreter   string `json:"interpreter,omitempty"`
}

// PackageObservation captures package-launch and CLI diagnostics observed on the host.
type PackageObservation struct {
	InventoryMatchesImage       bool   `json:"inventory_matches_image"`
	SilentlyMissingResource     string `json:"silently_missing_resource,omitempty"`
	CLIResolvedToSystem         string `json:"cli_resolved_to_system,omitempty"`
	MisreportedAsMissingPackage bool   `json:"misreported_as_missing_package,omitempty"`
	AuthorityViolation          string `json:"authority_violation,omitempty"`
}

// Observation holds independent host-side evidence collected after running a CaseSpec.
type Observation struct {
	CaseID          string               `json:"case_id"`
	Route           RouteRecord          `json:"route"`
	ViewedSentinels map[string]string    `json:"viewed_sentinels,omitempty"`
	Receipts        []PublicationReceipt `json:"receipts,omitempty"`
	Citations       []CitationRecord     `json:"citations,omitempty"`
	Activities      []ActivityRecord     `json:"activities,omitempty"`
	FinalFiles      map[string][]byte    `json:"final_files,omitempty"`
	WorkspaceFiles  []string             `json:"workspace_files,omitempty"`
	Package         PackageObservation   `json:"package,omitempty"`
	TerminalOutcome string               `json:"terminal_outcome,omitempty"`
	Stdout          string               `json:"stdout,omitempty"`
}

// GradeReport holds independent per-guarantee verdicts and version metadata.
type GradeReport struct {
	CaseID        string                         `json:"case_id"`
	CaseVersion   string                         `json:"case_version"`
	OracleVersion string                         `json:"oracle_version"`
	GraderVersion string                         `json:"grader_version"`
	Verdicts      map[Guarantee]GuaranteeVerdict `json:"verdicts"`
}

// Pass returns true when no guarantee has VerdictFail.
func (r GradeReport) Pass() bool {
	for _, v := range r.Verdicts {
		if v.Status == VerdictFail {
			return false
		}
	}
	return true
}

// Verdict returns the recorded GuaranteeVerdict for g.
func (r GradeReport) Verdict(g Guarantee) GuaranteeVerdict {
	return r.Verdicts[g]
}
