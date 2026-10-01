package project

// SourceFileWritable reports whether mode bits grant owner write (0200).
func SourceFileWritable(mode uint32) bool {
	return mode&0o200 != 0
}
