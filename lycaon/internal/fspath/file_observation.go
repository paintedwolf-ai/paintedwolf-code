package fspath

// FileObservation binds content facts to native identity, change metadata, and size.
type FileObservation struct {
	Identity AllocationIdentity
	Changed  int64
	Size     int64
	Links    uint64
	Known    bool
}
