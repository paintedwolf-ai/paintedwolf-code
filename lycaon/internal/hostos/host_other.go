//go:build !darwin && !linux

package hostos

var declared = Capabilities{}

func platformSystem() (System, error)            { return System{}, ErrUnsupported }
func platformIdentity() (Identity, error)        { return Identity{}, ErrUnsupported }
func platformCurrentIDs() (int, int)             { return 0, 0 }
func platformLookupUser(string) (int, error)     { return 0, ErrUnsupported }
func platformLookupGroup(string) (int, error)    { return 0, ErrUnsupported }
func platformFileOwner(string) (int, int, error) { return 0, 0, ErrUnsupported }
