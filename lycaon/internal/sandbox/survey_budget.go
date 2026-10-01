package sandbox

// SurveyBudgets bound traversal work; zero fields disable their bounds.
type SurveyBudgets struct {
	// DirectoryEntries rejects listings that exceed the cap.
	DirectoryEntries int
	// SubtreeEntries stops descent at the cap and retains observed entries.
	SubtreeEntries int
	// WalkEntries caps the whole traversal.
	WalkEntries int
}

// Bounded reports whether any bound is set.
func (b SurveyBudgets) Bounded() bool {
	return b.DirectoryEntries > 0 || b.SubtreeEntries > 0 || b.WalkEntries > 0
}

// BoundaryReason names why a traversal did not enter a directory.
type BoundaryReason string

const (
	// BoundaryUnreadable records an IO failure without inventing an empty subtree.
	BoundaryUnreadable BoundaryReason = "unreadable"
	// BoundaryDirectoryCap means the directory listing exceeded DirectoryEntries.
	BoundaryDirectoryCap BoundaryReason = "directory_cap"
	// BoundarySubtreeCap means the entries below the directory exceeded SubtreeEntries.
	BoundarySubtreeCap BoundaryReason = "subtree_cap"
	// BoundaryWalkBudget means the traversal spent WalkEntries before reaching the subtree.
	BoundaryWalkBudget BoundaryReason = "walk_budget"
	// BoundaryScope means the caller's SurveyScope declined the directory; Detail carries its reason.
	BoundaryScope BoundaryReason = "scope"
)

// SurveyBoundary is one directory a traversal did not observe completely.
type SurveyBoundary struct {
	// Rel is the directory's slash-separated root-relative path; "." is the root.
	Rel    string
	Reason BoundaryReason
	// Detail is the scope's own reason for BoundaryScope, otherwise empty.
	Detail string
	// Entries counts what the traversal read at or below Rel before the cut:
	// the listing size for a directory cap, the observed subtree for the other
	// caps. Zero when the directory was never opened.
	Entries int
}

// WalkBudget shares entry accounting across traversal implementations.
type WalkBudget struct {
	limits   SurveyBudgets
	observed int
	// subtree holds the entries observed below each open ancestor, root first.
	subtree []int
}

// NewWalkBudget starts accounting for one traversal.
func NewWalkBudget(limits SurveyBudgets) *WalkBudget {
	return &WalkBudget{limits: limits}
}

// ListingLimit is how many entries a listing may read before a directory is
// known to exceed DirectoryEntries: the cap plus one. Zero means unbounded.
func (b *WalkBudget) ListingLimit() int {
	if b == nil || b.limits.DirectoryEntries <= 0 {
		return 0
	}
	return b.limits.DirectoryEntries + 1
}

// Observed reports the entries the traversal has read so far.
func (b *WalkBudget) Observed() int {
	if b == nil {
		return 0
	}
	return b.observed
}

// EnterDir opens one directory for accounting. Depth is the number of open
// ancestors after the call; the root is depth 1.
func (b *WalkBudget) EnterDir() int {
	if b == nil {
		return 0
	}
	b.subtree = append(b.subtree, 0)
	return len(b.subtree)
}

// LeaveDir closes the deepest open directory and returns the entries observed
// below it.
func (b *WalkBudget) LeaveDir() int {
	if b == nil || len(b.subtree) == 0 {
		return 0
	}
	last := b.subtree[len(b.subtree)-1]
	b.subtree = b.subtree[:len(b.subtree)-1]
	return last
}

// SubtreeObserved reports the entries observed below the deepest open directory.
func (b *WalkBudget) SubtreeObserved() int {
	if b == nil || len(b.subtree) == 0 {
		return 0
	}
	return b.subtree[len(b.subtree)-1]
}

// Charge counts entries against the walk and each open directory.
// It returns 0 for an exhausted walk, the shallowest exceeded subtree depth,
// or -1 while all budgets hold.
func (b *WalkBudget) Charge(n int) (int, BoundaryReason) {
	if b == nil || n <= 0 {
		return -1, ""
	}
	b.observed += n
	for i := range b.subtree {
		b.subtree[i] += n
	}
	if b.limits.WalkEntries > 0 && b.observed > b.limits.WalkEntries {
		return 0, BoundaryWalkBudget
	}
	if b.limits.SubtreeEntries > 0 {
		// The root's subtree is the walk; WalkEntries bounds it.
		for i := 1; i < len(b.subtree); i++ {
			if b.subtree[i] > b.limits.SubtreeEntries {
				return i + 1, BoundarySubtreeCap
			}
		}
	}
	return -1, ""
}

// Exhausted reports whether the walk budget has been spent.
func (b *WalkBudget) Exhausted() bool {
	return b != nil && b.limits.WalkEntries > 0 && b.observed > b.limits.WalkEntries
}
