//go:build darwin && cgo

package watchfd

// Recursive reports that one registration observes a whole tree. The FSEvents
// stream on a root delivers every write beneath it, so coverage is complete
// the moment the root stream starts.
const Recursive = true

// EntryDescriptors reports whether registrations charge for directory children.
const EntryDescriptors = false

// Cost reports the logical cost of one directory under the recursive native
// FSEvents stream. The stream uses no descriptor per file or directory.
func Cost(int) int { return 1 }

// poolDescriptors keeps a finite logical registration budget so bookkeeping
// cannot grow without bound.
func poolDescriptors() int { return maxDescriptors }

const maxDescriptors = 16384
