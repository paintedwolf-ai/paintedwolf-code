package localdata

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editoroutbox"
)

var (
	storeCoupledRelDirs  = entryNames(func(e ConfigRootEntry) bool { return e.IsDir && e.StoreCoupled })
	storeCoupledRelPaths = entryNames(func(e ConfigRootEntry) bool { return !e.IsDir && e.StoreCoupled })
)

// FirstRunOnboardingRelPath is the app-state slice reset by Start fresh.
func FirstRunOnboardingRelPath() string {
	name := hex.EncodeToString([]byte("onboarding")) + ".json"
	return filepath.ToSlash(filepath.Join(appStateDirName, name))
}

// StoreResetRecoveryRelPaths returns files preserved before a store reset.
func StoreResetRecoveryRelPaths() []string {
	return append(append([]string(nil), storeCoupledRelPaths...), FirstRunOnboardingRelPath())
}

// StoreResetRecoveryRelDirs returns identity-coupled directories preserved before reset.
func StoreResetRecoveryRelDirs() []string {
	return append([]string(nil), storeCoupledRelDirs...)
}

// ResetStoreCoupled removes development state keyed by database identities.
func ResetStoreCoupled(ctx context.Context, dbPath string) error {
	dbPath = filepath.Clean(strings.TrimSpace(dbPath))
	if dbPath == "" || dbPath == "." {
		return nil
	}
	base := filepath.Dir(dbPath)
	release, err := editoroutbox.Acquire(ctx, base)
	if err != nil {
		return err
	}
	defer release()
	var errs []error
	if err := db.RemoveStore(dbPath); err != nil {
		errs = append(errs, fmt.Errorf("remove store: %w", err))
	}
	errs = append(errs, resetStoreDependents(base)...)
	return errors.Join(errs...)
}

// ResetStoreDependents removes state whose identities belong to a replaced store.
func ResetStoreDependents(dbPath string) error {
	dbPath = filepath.Clean(strings.TrimSpace(dbPath))
	if dbPath == "" || dbPath == "." {
		return nil
	}
	return errors.Join(resetStoreDependents(filepath.Dir(dbPath))...)
}

func resetStoreDependents(base string) []error {
	var errs []error
	for _, rel := range storeCoupledRelPaths {
		if isStoreFile(rel) {
			continue
		}
		path := filepath.Join(base, rel)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	for _, rel := range storeCoupledRelDirs {
		path := filepath.Join(base, rel)
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return errs
}
