package sourcecatalog

// DirectoryStamp is a directory inode's own modification and change times in
// nanoseconds. Adding, removing, or renaming an entry moves both, so a listing
// recorded with a stamp still describes the directory while the stamp matches.
type DirectoryStamp struct {
	Modified int64
	Changed  int64
}

func (s DirectoryStamp) known() bool { return s != DirectoryStamp{} }
