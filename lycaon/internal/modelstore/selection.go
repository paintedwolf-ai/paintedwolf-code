package modelstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/filelock"
)

type Selection struct {
	State    string
	Model    string
	Device   string
	Revision string
}

func (s *Store) Selection() (Selection, error) {
	h, err := s.history()
	if err == nil && h.selection.State == "unconfigured" && len(h.visible) > 0 {
		m := h.visible[len(h.visible)-1]
		return Selection{State: "selected", Model: m.Model, Device: m.Device, Revision: m.Revision}, nil
	}
	if h.selection.State == "evicted" {
		return Selection{State: "unconfigured"}, err
	}
	return h.selection, err
}

// The journal lock prevents new leases during the selection commit.
func (s *Store) checkEvictable(history []Manifest, keep *Manifest) error {
	for _, m := range history {
		if keep != nil && sameManifest(m, *keep) {
			continue
		}
		f, err := s.revisionLock(m)
		if err != nil {
			return err
		}
		ok, err := filelock.TryExclusive(f)
		if err = errors.Join(err, f.Close()); err != nil {
			return err
		}
		if !ok {
			return ErrRevisionInUse
		}
	}
	return nil
}

func (s *Store) provisioningLock() (*os.File, error) {
	f, err := filelock.Open(filepath.Join(s.root, "provision.lock"))
	if err != nil {
		return nil, err
	}
	held, err := filelock.TryExclusive(f)
	if !held && err == nil {
		err = errors.New("modelstore: another provisioning operation is running")
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func (s *Store) PurgeAll() (resultErr error) {
	f, err := s.provisioningLock()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, f.Close()) }()
	if err := s.optOut(); err != nil {
		return err
	}
	return s.finishSelection()
}

func (s *Store) optOut() error {
	lock, err := s.lockJournal()
	if err != nil {
		return err
	}
	defer lock.Unlock()
	if err := s.recoverJournal(); err != nil {
		return err
	}
	h, err := s.history()
	if err != nil {
		return err
	}
	if err := s.checkEvictable(h.all, nil); err != nil {
		return err
	}
	if h.selection.State == "opted_out" {
		return nil
	}
	return s.appendRecord(journalRecord{Version: 2, Selection: "opted_out"})
}

func (s *Store) finishSelection() error {
	if _, err := s.Collect(); err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	return errors.Join(root.RemoveAll("partials"), root.Close())
}

func (s *Store) install(ctx context.Context, signed SignedManifest, resumable bool, beforeEviction func() error, stage func(Artifact) error) (resultErr error) {
	if err := validate(signed.Manifest); err != nil {
		return err
	}
	if s.signer == nil {
		return errors.New("modelstore: missing release authority")
	}
	if err := s.signer.Verify(signed); err != nil {
		return err
	}
	pins, err := s.pinImport(signed.Manifest, false)
	if err != nil {
		return err
	}
	defer func() {
		for _, f := range pins {
			resultErr = errors.Join(resultErr, f.Close())
		}
	}()
	if err := s.admitInstall(ctx, signed.Manifest, resumable, beforeEviction); err != nil {
		return err
	}
	for _, a := range append([]Artifact{signed.Manifest.Artifact}, signed.Manifest.Files...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := stage(a); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.publish(signed, false, true); err != nil {
		return err
	}
	return s.finishSelection()
}
