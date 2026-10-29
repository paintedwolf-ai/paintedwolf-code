package fspath

// AllocationIdentity identifies hard-linked entries during one observed walk.
type AllocationIdentity struct{ Device, Inode uint64 }

// Allocation is an observed entry's allocated 512-byte storage. Known is false
// when the platform does not supply allocation facts; Bytes is never estimated.
type Allocation struct {
	Identity AllocationIdentity
	Bytes    int64
	Links    uint64
	Known    bool
}
