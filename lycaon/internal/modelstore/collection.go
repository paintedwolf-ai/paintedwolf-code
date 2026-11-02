package modelstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/lycaon/lycaon/internal/filelock"
)

// Collect removes unreferenced artifacts, retaining active, compatible previous and leased revisions.
func (s *Store) Collect() (removed []string, resultErr error) {
	lock, err := s.lockJournal()
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()

	if err := s.recoverJournal(); err != nil {
		return nil, err
	}
	records, err := s.readRecords()
	if err != nil {
		return nil, err
	}

	type slotHistory struct {
		active   *Manifest
		previous *Manifest
	}
	slots := map[string]*slotHistory{}
	for i := range records {
		m := records[i]
		key := m.Model + "/" + m.Device
		sh := slots[key]
		if sh == nil {
			sh = &slotHistory{}
			slots[key] = sh
		}
		if sh.active != nil && sh.active.Revision != m.Revision {
			prev := *sh.active
			sh.previous = &prev
		}
		curr := m
		sh.active = &curr
	}

	keep := map[string]bool{}
	keepRevisions := map[string]bool{}
	keepManifest := func(m *Manifest) {
		if m == nil {
			return
		}
		keepRevisions[s.revisionRel(*m)] = true
		keep[m.Artifact.Digest] = true
		for _, f := range m.Files {
			keep[f.Digest] = true
		}
	}
	for _, sh := range slots {
		keepManifest(sh.active)
		keepManifest(sh.previous)
	}

	for _, m := range records {
		f, err := s.revisionLock(m)
		if err != nil {
			return nil, err
		}
		held, err := filelock.TryExclusive(f)
		if err != nil {
			return nil, errors.Join(err, f.Close())
		}
		if !held {
			keepManifest(&m)
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}

	for _, m := range records {
		rel := s.revisionRel(m)
		if !keepRevisions[rel] {
			root, err := s.openRoot()
			if err != nil {
				return nil, err
			}
			err = errors.Join(root.RemoveAll(rel), root.Close())
			if err != nil {
				return nil, err
			}
		}
	}

	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()

	artifactsDir := filepath.Join(s.root, "artifacts")
	prefixEntries, err := os.ReadDir(artifactsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("modelstore: readdir artifacts: %w", err)
	}

	for _, prefixEntry := range prefixEntries {
		if !prefixEntry.IsDir() {
			continue
		}
		prefix := prefixEntry.Name()
		subEntries, err := os.ReadDir(filepath.Join(artifactsDir, prefix))
		if err != nil {
			return nil, fmt.Errorf("modelstore: readdir %s: %w", prefix, err)
		}
		for _, entry := range subEntries {
			name := entry.Name()
			if !digestShape.MatchString(name) {
				continue
			}
			if keep[name] {
				continue
			}
			pin, err := s.artifactLock(name)
			if err != nil {
				return nil, err
			}
			held, err := filelock.TryExclusive(pin)
			if err != nil || !held {
				err = errors.Join(err, pin.Close())
				if err != nil {
					return nil, err
				}
				continue
			}
			err = errors.Join(root.Remove(filepath.Join("artifacts", prefix, name)), pin.Close())
			if err != nil {
				return nil, fmt.Errorf("modelstore: remove %s: %w", name, err)
			}
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	return removed, nil
}
