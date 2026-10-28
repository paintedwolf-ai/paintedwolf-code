//go:build windows

package fseffect

import "os"

// Stat reports metadata through the handle-relative reader, which refuses
// reparse traversal; owner, inode and link facts are not POSIX values here.
func (r *ReadRoot) Stat(rel string, _ bool, admit func(string) error) (EntryStat, error) {
	file, err := r.OpenExact(rel)
	if err != nil {
		return EntryStat{}, err
	}
	defer file.Close()
	if admit != nil {
		if err := admit(file.Name()); err != nil {
			return EntryStat{}, err
		}
	}
	info, err := file.Stat()
	if err != nil {
		return EntryStat{}, err
	}
	return EntryStat{Name: info.Name(), Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime().UTC()}, nil
}

// StatChildAt reports an immediate child's metadata through the held root.
func (r *ReadRoot) StatChildAt(_ *os.File, childRel, _ string, admit func(string) error) (EntryStat, error) {
	return r.Stat(childRel, false, admit)
}
