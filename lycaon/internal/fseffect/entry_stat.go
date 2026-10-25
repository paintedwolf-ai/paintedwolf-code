package fseffect

import (
	"io/fs"
	"time"
)

// EntryStat is one entry's metadata observed beneath a held read root. POSIX
// reports whether owner, inode and link facts are native POSIX values; a
// platform without them never pretends to have them.
type EntryStat struct {
	Name    string
	Mode    fs.FileMode
	Size    int64
	ModTime time.Time
	POSIX   bool
	UID     uint32
	GID     uint32
	Inode   uint64
	Links   uint64
	Device  uint64
}
