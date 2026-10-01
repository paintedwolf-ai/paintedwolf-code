package api

// Board character budget constants (bundled pack-board limits).
const (
	MaxBoardInjectChars  = 320
	MaxBoardCompactChars = 480
	MaxBoardDetailChars  = 1200
)

// BoardDetailLevel selects how much board detail a read renders.
type BoardDetailLevel string

const (
	BoardDetailLevelStatus   BoardDetailLevel = "status"
	BoardDetailLevelCompact  BoardDetailLevel = "compact"
	BoardDetailLevelFull     BoardDetailLevel = "full"
	BoardDetailLevelForensic BoardDetailLevel = "forensic"
)

// BoardForensicNotice warns consumers that forensic_workers is host-debug payload.
const BoardForensicNotice = "Full worker job records including workspace baselines. Host-debug only."

// BoardReservationEntry is one active handoff_reserve hold surfaced in pack_board text.
type BoardReservationEntry struct {
	Path     string `json:"path"`
	JobID    string `json:"job_id"`
	LegLabel string `json:"leg_label,omitempty"`
}

// BoardDelegationSlice holds delegation board data keyed by section, such as delegations.
type BoardDelegationSlice map[string]any

// BoardWorkersSlice holds worker board data keyed by section, such as tasks.
type BoardWorkersSlice map[string]any

// BoardHostSlice carries sidecar host facts for pack board orientation.
type BoardHostSlice struct {
	OS              string          `json:"os"`
	Arch            string          `json:"arch"`
	ExecutionTarget ExecutionTarget `json:"execution_target,omitempty"`
	Shell           bool            `json:"shell"`
	Toolchains      []string        `json:"toolchains,omitempty"`
}

// BoardOrientationRoot is one root section in a multi-root board orientation inject.
type BoardOrientationRoot struct {
	Label     string    `json:"label"`
	Path      string    `json:"path,omitempty"`
	IsPrimary bool      `json:"is_primary"`
	Brief     RepoBrief `json:"brief"`
	Truncated bool      `json:"truncated,omitempty"`
}

// BoardSnapshot is the host-internal assembly DTO (builder output). Serialize BoardView on the wire.
type BoardSnapshot struct {
	Repo              RepoBrief              `json:"repo"`
	OrientationRoots  []BoardOrientationRoot `json:"orientation_roots,omitempty"`
	Host              *BoardHostSlice        `json:"host,omitempty"`
	Delegation        *BoardDelegationSlice  `json:"delegation,omitempty"`
	Workers           *BoardWorkersSlice     `json:"workers,omitempty"`
	Git               *BoardGitSlice         `json:"git,omitempty"`
	Scans             *BoardScansSlice       `json:"scans,omitempty"`
	Cost              *CostSummary           `json:"cost"`
	ActiveWorkflowRun *WorkflowRun           `json:"active_workflow_run,omitempty"`
	PackContentHash   string                 `json:"pack_content_hash"`
	DetailLevel       BoardDetailLevel       `json:"detail_level"`
}
