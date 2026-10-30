package fspath

// FileObservation identifies file changes that restored size and mtime cannot hide.
type FileObservation struct {
	Identity AllocationIdentity
	Changed  int64
	Links    uint64
	Known    bool
}
