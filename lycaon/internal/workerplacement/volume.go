package workerplacement

// Volume identifies one mounted filesystem. ID is stable only while it is mounted.
type Volume struct {
	ID   uint64
	Root string
}
