package modelstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/fssync"
)

// Activate verifies manifest authority and appends the manifest to the activation journal under lock.
func (s *Store) Activate(signed SignedManifest) error {
	return s.activate(signed, false)
}

func (s *Store) activate(signed SignedManifest, repair bool) error {
	if s.signer == nil {
		return errors.New("modelstore: no manifest authority configured")
	}
	if err := s.signer.Verify(signed); err != nil {
		return fmt.Errorf("modelstore: manifest authority: %w", err)
	}
	m := signed.Manifest
	if err := validate(m); err != nil {
		return err
	}
	lock, err := s.lockJournal()
	if err != nil {
		return err
	}
	defer lock.Unlock()

	if !s.Verified(m.Artifact.Digest) {
		return fmt.Errorf("modelstore: artifact %s is not verified", m.Artifact.Digest)
	}
	for _, f := range m.Files {
		if !s.Verified(f.Digest) {
			return fmt.Errorf("modelstore: supplementary artifact %s is not verified", f.Digest)
		}
	}
	if err := s.recoverJournal(); err != nil {
		return err
	}
	records, err := s.readRecords()
	if err != nil {
		return err
	}
	for _, previous := range records {
		if s.revisionRel(previous) == s.revisionRel(m) && !sameManifest(previous, m) {
			return fmt.Errorf("modelstore: revision identity collision for %s", s.revisionRel(m))
		}
	}
	if _, err := s.materializeRevision(m, repair); err != nil {
		return err
	}
	line, err := json.Marshal(journalRecord{Version: 1, Signed: signed})
	if err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile("journal.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size()+int64(len(line))+1 > MaxJournalBytes {
		return errors.New("modelstore: journal exceeds recovery bound")
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("modelstore: append: %w", err)
	}
	if err := fssync.File(f); err != nil {
		return fmt.Errorf("modelstore: journal sync: %w", err)
	}
	if dfd, err := os.Open(s.root); err != nil {
		return fmt.Errorf("modelstore: journal dir: %w", err)
	} else if err := fssync.File(dfd); err != nil {
		dfd.Close()
		return fmt.Errorf("modelstore: journal dir sync: %w", err)
	} else if err := dfd.Close(); err != nil {
		return fmt.Errorf("modelstore: journal dir close: %w", err)
	}
	return nil
}
