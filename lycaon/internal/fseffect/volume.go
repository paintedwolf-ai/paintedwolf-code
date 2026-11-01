package fseffect

// Volume is the file system holding an open file. Block counts are in units
// of BlockSize bytes; Source and Target name the mounted device and its
// mount point.
type Volume struct {
	Source, Target          string
	BlockSize               uint64
	Blocks, Free, Available uint64
	Files, FreeFiles        uint64
}
