package modelstore

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/filelock"
)

// AcquireLease pins one immutable revision across store instances and processes.
func (s *Store) AcquireLease(model, device, owner string, generation uint64) (*Lease, error) {
	lock, err := s.lockJournal()
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	active, err := s.Active()
	if err != nil {
		return nil, err
	}
	m, ok := active[model+"/"+device]
	if !ok {
		return nil, fmt.Errorf("modelstore: no active manifest for %s/%s", model, device)
	}
	dir, err := s.checkRevision(m)
	if err != nil {
		return nil, err
	}
	f, err := s.revisionLock(m)
	if err != nil {
		return nil, err
	}
	held, err := filelock.TryShared(f)
	if err != nil || !held {
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("modelstore: cannot lease revision: %w", err)
		}
		return nil, ErrRevisionInUse
	}
	return &Lease{lock: f, Manifest: m, Owner: owner, Generation: generation, Dir: dir}, nil
}

// Lock files retain their inode after a revision is collected.
func (s *Store) revisionLock(m Manifest) (*os.File, error) {
	return filelock.Open(filepath.Join(s.root, "locks", m.Model, m.Device, m.Revision+".lock"))
}
