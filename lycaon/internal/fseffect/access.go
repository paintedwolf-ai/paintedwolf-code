package fseffect

// AccessMode selects an effective-identity OS access query, never a mutation.
type AccessMode uint8

const (
	ReadAccess AccessMode = iota + 1
	WriteAccess
	ExecuteAccess
)
