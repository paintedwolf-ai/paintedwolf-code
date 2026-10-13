package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/lycaon/lycaon/internal/vaultidentity"
)

var (
	errNotFound = errors.New("identity not found")
	errExists   = errors.New("identity exists")
)

// Store keeps identities as owner-only files under one directory per uid.
// Only this service's dynamic user can read the tree.
type Store struct {
	Root string
}

func (s *Store) userDir(uid int) string { return filepath.Join(s.Root, strconv.Itoa(uid)) }

func (s *Store) path(uid int, account string) string {
	return filepath.Join(s.userDir(uid), account+".identity")
}

// Read returns the stored identity.
func (s *Store) Read(uid int, account string) ([]byte, error) {
	file, err := os.Open(s.path(uid, account))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNotFound
		}
		return nil, err
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, vaultidentity.MaxValueBytes+1))
	if err != nil {
		return nil, err
	}
	if len(value) == 0 || len(value) > vaultidentity.MaxValueBytes {
		return nil, fmt.Errorf("stored identity has %d bytes", len(value))
	}
	return value, nil
}

// Create publishes a new identity exclusively: the complete file is linked
// into place, so a concurrent creator sees either nothing or the winner.
func (s *Store) Create(uid int, account string, value []byte) error {
	if len(value) == 0 || len(value) > vaultidentity.MaxValueBytes {
		return fmt.Errorf("identity must be between 1 and %d bytes", vaultidentity.MaxValueBytes)
	}
	dir := s.userDir(uid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := filepath.Join(dir, "."+account+".tmp-"+hex.EncodeToString(nonce[:]))
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	_, err = file.Write(value)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Link(temp, s.path(uid, account)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errExists
		}
		return err
	}
	return syncDir(dir)
}

// Remove deletes the identity; a missing one is already removed.
func (s *Store) Remove(uid int, account string) error {
	if err := os.Remove(s.path(uid, account)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(s.userDir(uid))
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer handle.Close()
	// Some filesystems refuse directory sync; the link itself is durable enough.
	_ = handle.Sync()
	return nil
}
