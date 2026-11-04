package adapter

import (
	"context"
	"fmt"
)

// FilesystemClass is the part of a budget's class that clone availability decides.
type FilesystemClass string

const (
	FilesystemClonable FilesystemClass = "clonable"
	FilesystemCopyOnly FilesystemClass = "copy_only"
)

// BudgetClass names one recorded break-even.
type BudgetClass struct {
	Platform   string          `json:"platform"`
	Filesystem FilesystemClass `json:"filesystem"`
}

// BudgetFact is a recorded break-even between materializing and mounting. Costs are in bytes, and
// EntryCost is what one entry in one tree weighs against them.
type BudgetFact struct {
	Class       BudgetClass `json:"class"`
	Limit       int64       `json:"limit"`
	EntryCost   int64       `json:"entry_cost"`
	Provisional bool        `json:"provisional"`
}

const (
	provisionalEntryCost int64 = 4096
	// A clone pays for entries only. Two trees of 200k entries is the provisional line.
	provisionalCloneLimit = 2 * 200_000 * provisionalEntryCost
	// A byte copy also holds its 5 GiB of content twice.
	provisionalCopyLimit = provisionalCloneLimit + 2*5*1024*1024*1024
)

// budgetFacts holds one fact per platform and filesystem class. #1667 replaces these provisional
// values with measured ones; nothing edits them at runtime.
var budgetFacts = map[BudgetClass]BudgetFact{
	{Platform: "darwin", Filesystem: FilesystemClonable}: {Limit: provisionalCloneLimit, EntryCost: provisionalEntryCost, Provisional: true},
	{Platform: "darwin", Filesystem: FilesystemCopyOnly}: {Limit: provisionalCopyLimit, EntryCost: provisionalEntryCost, Provisional: true},
	{Platform: "linux", Filesystem: FilesystemClonable}:  {Limit: provisionalCloneLimit, EntryCost: provisionalEntryCost, Provisional: true},
	{Platform: "linux", Filesystem: FilesystemCopyOnly}:  {Limit: provisionalCopyLimit, EntryCost: provisionalEntryCost, Provisional: true},
}

// RecordedBudget returns the fact for class. A platform with no mount has no break-even to measure,
// so it reads as the same class on the nearest platform.
func RecordedBudget(class BudgetClass) BudgetFact {
	fact, ok := budgetFacts[class]
	if !ok {
		fact = budgetFacts[BudgetClass{Platform: "linux", Filesystem: class.Filesystem}]
	}
	fact.Class = class
	return fact
}

// ZeroBudget puts every tree over budget. Fixtures that exercise a mounted view use it.
func ZeroBudget(class BudgetClass) BudgetFact {
	return BudgetFact{Class: class, Limit: -1, EntryCost: 1}
}

// CostSource says where a materialize cost came from.
type CostSource string

const (
	CostFromIndex  CostSource = "index"
	CostFromSurvey CostSource = "survey"
	// CostFromCopy is what a copy observed before it passed the budget.
	CostFromCopy CostSource = "copy"
)

// MaterializeCost counts both trees: every entry twice, and every byte that cannot be cloned twice.
type MaterializeCost struct {
	Entries          int64      `json:"entries"`
	UncloneableBytes int64      `json:"uncloneable_bytes"`
	Source           CostSource `json:"source"`
	// Partial means counting stopped at the budget, so the totals are only a lower bound.
	Partial bool `json:"partial,omitempty"`
}

// Total is the cost measured against a budget and the disk space the two trees need.
func (c MaterializeCost) Total(fact BudgetFact) int64 {
	return 2 * (c.Entries*fact.EntryCost + c.UncloneableBytes)
}

// SelectionReason says why one worker got its provider.
type SelectionReason string

const (
	SelectionUnderBudget        SelectionReason = "under_budget"
	SelectionOverBudgetMounted  SelectionReason = "over_budget_mounted"
	SelectionOverBudgetMaterial SelectionReason = "over_budget_mount_unavailable"
)

// SelectionFacts record one worker's selection.
type SelectionFacts struct {
	Provider AdapterID       `json:"provider"`
	Reason   SelectionReason `json:"reason"`
	// MountUnavailable is the lasting reason a mount could not serve an over-budget worker.
	MountUnavailable ReasonCode      `json:"mount_unavailable,omitempty"`
	CloneAvailable   bool            `json:"clone_available"`
	DeclaredCost     MaterializeCost `json:"declared_cost"`
	Budget           BudgetFact      `json:"budget"`
	// CopyPassedBudget means a copy passed the budget and cleaned up before this selection.
	CopyPassedBudget bool `json:"copy_passed_budget,omitempty"`
}

// CopyBudget stops a materialization that passes the budget. A nil budget is the declared
// cannot-mount case, which has nowhere further to go.
type CopyBudget struct {
	Limit     int64
	EntryCost int64
}

func (b *CopyBudget) passedBy(entries, copiedBytes int64) bool {
	if b == nil {
		return false
	}
	return 2*entries*b.EntryCost+copiedBytes > b.Limit
}

// BudgetOverrunError reports a copy that passed its budget. Only its own incomplete trees are gone
// by the time a caller sees it.
type BudgetOverrunError struct {
	ViewID      string
	RootID      string
	Entries     int64
	CopiedBytes int64
	Limit       int64
}

func (e *BudgetOverrunError) Error() string {
	return fmt.Sprintf("materializing view %s passed its budget in root %s: %d entries and %d copied bytes against %d",
		e.ViewID, e.RootID, e.Entries, e.CopiedBytes, e.Limit)
}

// DiskSpace is what the volume holding a worker's trees has free.
type DiskSpace struct {
	Path      string
	Available uint64
	Known     bool
}

// WorkspaceCapacityError refuses a workspace the disk cannot hold. It is the only refusal selection
// makes, and it carries the numbers a person needs to choose a smaller workspace.
type WorkspaceCapacityError struct {
	Path              string
	Required          uint64
	Available         uint64
	SelectedRoots     []string
	SuggestedSubroots []string
}

func (e *WorkspaceCapacityError) Error() string {
	return fmt.Sprintf("%s: this workspace needs %d bytes and %s has %d", ReasonWorkspaceDoesNotFit, e.Required, e.Path, e.Available)
}

// CloneAvailability says whether a root's trees can be cloned where this worker's branch is placed.
// Placement supplies the real answer; ProbeCloneAt is the probe behind it today.
type CloneAvailability func(ctx context.Context, grant RootGrant) (bool, error)

// lastingMountReasons are failures a retry would not change, so a large copy may be the answer.
var lastingMountReasons = map[ReasonCode]bool{
	ReasonUnsupportedKernel:        true,
	ReasonUnsupportedPlatform:      true,
	ReasonMountDenied:              true,
	ReasonFUSEDeviceUnavailable:    true,
	ReasonFUSEHelperUnavailable:    true,
	ReasonFUSEMountDenied:          true,
	ReasonConfinementUnavailable:   true,
	ReasonHelperMissing:            true,
	ReasonMountHelperUnavailable:   true,
	ReasonBackingConfinementFailed: true,
	ReasonProbeReadWriteFailed:     true,
}

// IsLastingMountReason reports whether reason means the mount stays unavailable for this worker.
func IsLastingMountReason(reason ReasonCode) bool { return lastingMountReasons[reason] }
