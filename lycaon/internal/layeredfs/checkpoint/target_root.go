package checkpoint

import (
	"io/fs"
	"os"
	"sort"
	"sync"
)

// A promotion's target root is often on a remote volume, where every operation is a round trip.
// targetRoot counts each one by kind, and a transaction holds one validated root per root ID for
// its whole run instead of opening and checking the root again for every path.

// SourceOps counts the operations a promotion made on its target roots, by kind.
type SourceOps struct {
	mu     sync.Mutex
	counts map[string]int64
}

func (o *SourceOps) add(kind string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if o.counts == nil {
		o.counts = map[string]int64{}
	}
	o.counts[kind]++
	o.mu.Unlock()
}

// Counts returns the operations so far by kind.
func (o *SourceOps) Counts() map[string]int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make(map[string]int64, len(o.counts))
	for kind, n := range o.counts {
		out[kind] = n
	}
	return out
}

// Total is every operation so far.
func (o *SourceOps) Total() int64 {
	var total int64
	for _, n := range o.Counts() {
		total += n
	}
	return total
}

// Kinds lists the kinds counted so far, for reporting.
func (o *SourceOps) Kinds() []string {
	counts := o.Counts()
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// targetRoot is an open target root whose operations are counted. A held root stays open for the
// transaction, so closing it here does nothing.
type targetRoot struct {
	root *os.Root
	held bool
	ops  *SourceOps
}

func (r *targetRoot) Close() error {
	if r.held {
		return nil
	}
	return r.root.Close()
}

func (r *targetRoot) Name() string { return r.root.Name() }

func (r *targetRoot) FS() fs.FS { return r.root.FS() }

func (r *targetRoot) Lstat(name string) (fs.FileInfo, error) {
	r.ops.add("lstat")
	return r.root.Lstat(name)
}

func (r *targetRoot) Stat(name string) (fs.FileInfo, error) {
	r.ops.add("stat")
	return r.root.Stat(name)
}

func (r *targetRoot) Open(name string) (*os.File, error) {
	r.ops.add("open")
	return r.root.Open(name)
}

func (r *targetRoot) OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error) {
	r.ops.add("open")
	return r.root.OpenFile(name, flag, perm)
}

func (r *targetRoot) ReadFile(name string) ([]byte, error) {
	r.ops.add("open")
	return r.root.ReadFile(name)
}

func (r *targetRoot) Readlink(name string) (string, error) {
	r.ops.add("readlink")
	return r.root.Readlink(name)
}

func (r *targetRoot) Mkdir(name string, perm fs.FileMode) error {
	r.ops.add("mkdir")
	return r.root.Mkdir(name, perm)
}

func (r *targetRoot) Remove(name string) error {
	r.ops.add("remove")
	return r.root.Remove(name)
}

func (r *targetRoot) RemoveAll(name string) error {
	r.ops.add("remove")
	return r.root.RemoveAll(name)
}

func (r *targetRoot) Rename(oldname, newname string) error {
	r.ops.add("rename")
	return r.root.Rename(oldname, newname)
}

func (r *targetRoot) Link(oldname, newname string) error {
	r.ops.add("link")
	return r.root.Link(oldname, newname)
}

func (r *targetRoot) Symlink(oldname, newname string) error {
	r.ops.add("symlink")
	return r.root.Symlink(oldname, newname)
}
