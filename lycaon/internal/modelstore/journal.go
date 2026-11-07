package modelstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fssync"
)

type journalRecord struct {
	Version int            `json:"version"`
	Signed  SignedManifest `json:"signed"`
}

func (s *Store) journalPath() string { return filepath.Join(s.root, "journal.jsonl") }

type journalLock struct{ f *os.File }

func (s *Store) lockJournal() (*journalLock, error) {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, err
	}
	f, err := filelock.Open(filepath.Join(s.root, "journal.lock"))
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		return nil, fmt.Errorf("modelstore: lock: %w", errors.Join(err, f.Close()))
	}
	return &journalLock{f}, nil
}

func (l *journalLock) Unlock() { _ = l.f.Close() }

func (s *Store) journalBytes() (data []byte, resultErr error) {
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	f, err := root.Open("journal.jsonl")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, f.Close()) }()
	raw, err := io.ReadAll(io.LimitReader(f, MaxJournalBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxJournalBytes {
		return nil, errors.New("modelstore: journal exceeds recovery bound")
	}
	return raw, nil
}

func (s *Store) parseRecords(raw []byte) ([]Manifest, int, error) {
	end := bytes.LastIndexByte(raw, '\n') + 1
	var records []Manifest
	for i, line := range bytes.Split(raw[:end], []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var record journalRecord
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return nil, 0, fmt.Errorf("modelstore: record %d: %w", i, err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, 0, fmt.Errorf("modelstore: trailing value in record %d", i)
		}
		if record.Version != 1 {
			return nil, 0, fmt.Errorf("modelstore: unknown journal version %d", record.Version)
		}
		if err := validate(record.Signed.Manifest); err != nil {
			return nil, 0, err
		}
		if s.signer == nil {
			return nil, 0, errors.New("modelstore: no manifest authority configured")
		}
		if err := s.signer.Verify(record.Signed); err != nil {
			return nil, 0, fmt.Errorf("modelstore: journal authority: %w", err)
		}
		records = append(records, record.Signed.Manifest)
	}
	return records, end, nil
}

// recoverJournal preserves complete records and removes only an interrupted append.
func (s *Store) recoverJournal() (resultErr error) {
	raw, err := s.journalBytes()
	if err != nil {
		return err
	}
	_, end, err := s.parseRecords(raw)
	if err != nil || end == len(raw) {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	f, err := root.OpenFile("journal.jsonl", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, f.Close()) }()
	if err := f.Truncate(int64(end)); err != nil {
		return err
	}
	return fssync.File(f)
}

func (s *Store) readRecords() ([]Manifest, error) {
	raw, err := s.journalBytes()
	if err != nil {
		return nil, err
	}
	records, _, err := s.parseRecords(raw)
	return records, err
}

// Active returns the newest authenticated manifest per model and device.
func (s *Store) Active() (map[string]Manifest, error) {
	records, err := s.readRecords()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Manifest)
	for _, m := range records {
		out[m.Model+"/"+m.Device] = m
	}
	return out, nil
}
