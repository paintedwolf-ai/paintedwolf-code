package harnessfixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// ReleaseWriteResources removes recorded outputs after artifact retention.
func ReleaseWriteResources(capture string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return releaseWriteResources(capture, filepath.Join(home, "shared-output"))
}

func releaseWriteResources(capture, base string) error {
	if !filepath.IsAbs(capture) {
		return fmt.Errorf("capture must be absolute")
	}
	receipts := filepath.Join(capture, "external-resources")
	entries, err := os.ReadDir(receipts)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			return fmt.Errorf("invalid external resource receipt")
		}
		body, err := os.ReadFile(filepath.Join(receipts, entry.Name()))
		if err != nil {
			return err
		}
		var owner WriteResource
		if err := json.Unmarshal(body, &owner); err != nil {
			return err
		}
		id, err := uuid.Parse(owner.ID)
		if err != nil || id.String() != owner.ID || entry.Name() != owner.ID+".json" || owner.Capture != capture || owner.Path != filepath.Join(base, owner.ID) {
			return fmt.Errorf("external resource ownership differs from its capture")
		}
		// Deletion stays inside the opened parent.
		info, err := os.Lstat(base)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("external output parent is not a directory")
		}
		root, err := os.OpenRoot(base)
		if err != nil {
			return err
		}
		opened, err := root.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = root.Close()
			return fmt.Errorf("external output parent changed while opening")
		}
		err = removeWriteResource(root, owner.ID)
		closeErr := root.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	return nil
}

func removeWriteResource(root *os.Root, id string) error {
	info, err := root.Lstat(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("external output is not a directory")
	}
	return root.RemoveAll(id)
}
