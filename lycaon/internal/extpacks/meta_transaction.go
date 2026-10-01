package extpacks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	metaTransactionPrefix = ".meta-pack-transaction-"
	metaTransactionMarker = "transaction.json"
	metaTransactionFormat = 1
	metaRemovalPrefix     = ".meta-pack-removal-"
)

type metaRootTransaction struct {
	Format      int    `json:"format"`
	Directory   string `json:"directory"`
	Staged      string `json:"staged"`
	Target      string `json:"target"`
	Backup      string `json:"backup"`
	DesiredPath string `json:"desired_path"`
	LockPath    string `json:"lock_path"`
	DesiredHash string `json:"desired_hash"`
	LockHash    string `json:"lock_hash"`
	Published   bool   `json:"published"`

	bound bool
}

func (t *metaRootTransaction) bind(desiredPath, lockPath string, desired, lock []byte) error {
	if t == nil {
		return nil
	}
	t.DesiredPath = desiredPath
	t.LockPath = lockPath
	t.DesiredHash = contentHash(desired)
	t.LockHash = contentHash(lock)
	if err := t.writeMarker(); err != nil {
		return err
	}
	t.bound = true
	return nil
}

func (t *metaRootTransaction) publish() error {
	if t == nil {
		return nil
	}
	if !t.bound {
		return fmt.Errorf("meta-pack cache transaction is not bound to state")
	}
	unlock := lockExtensionPath(t.Target)
	defer unlock()
	releaseFile, err := lockExtensionPathAcrossProcesses(t.Target)
	if err != nil {
		return err
	}
	defer releaseFile()

	if _, err := os.Stat(t.Target); err == nil {
		if err := os.Rename(t.Target, t.Backup); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(t.Staged, t.Target); err != nil {
		_ = restoreMetaRoot(t)
		return err
	}
	if err := syncExtensionDirectory(filepath.Dir(t.Target)); err != nil {
		_ = restoreMetaRoot(t)
		return err
	}
	t.Published = true
	if err := t.writeMarker(); err != nil {
		t.Published = false
		_ = restoreMetaRoot(t)
		return err
	}
	return nil
}

func (t *metaRootTransaction) writeMarker() error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return writeCacheMetadataFile(filepath.Join(t.Directory, metaTransactionMarker), append(data, '\n'))
}

func (t *metaRootTransaction) rollback() error {
	if t == nil {
		return nil
	}
	unlock := lockExtensionPath(t.Target)
	defer unlock()
	releaseFile, err := lockExtensionPathAcrossProcesses(t.Target)
	if err != nil {
		return err
	}
	defer releaseFile()
	return restoreMetaRoot(t)
}

func restoreMetaRoot(t *metaRootTransaction) error {
	_, stagedErr := os.Stat(t.Staged)
	_, backupErr := os.Stat(t.Backup)
	stagedExists := stagedErr == nil
	backupExists := backupErr == nil
	if stagedErr != nil && !os.IsNotExist(stagedErr) {
		return stagedErr
	}
	if backupErr != nil && !os.IsNotExist(backupErr) {
		return backupErr
	}

	if !stagedExists {
		if err := os.RemoveAll(t.Target); err != nil {
			return err
		}
	}
	if backupExists {
		if _, err := os.Stat(t.Target); err == nil {
			if err := os.RemoveAll(t.Target); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(t.Backup, t.Target); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(t.Directory); err != nil {
		return err
	}
	return syncExtensionDirectory(filepath.Dir(t.Target))
}

func (t *metaRootTransaction) finalize() error {
	if t == nil {
		return nil
	}
	if err := os.RemoveAll(t.Directory); err != nil {
		return err
	}
	return syncExtensionDirectory(filepath.Dir(t.Target))
}

func (t *metaRootTransaction) closeUnbound() {
	if t != nil && !t.bound {
		_ = os.RemoveAll(t.Directory)
	}
}

// RecoverMetaPackTransactions settles interrupted cache publications before discovery.
func RecoverMetaPackTransactions() error {
	root, err := MetaPackCacheRoot()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var recoveryErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if strings.HasPrefix(entry.Name(), metaRemovalPrefix) {
			if err := os.RemoveAll(dir); err != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("finalize %s: %w", entry.Name(), err))
			}
			continue
		}
		if !strings.HasPrefix(entry.Name(), metaTransactionPrefix) {
			continue
		}
		if err := recoverMetaPackTransaction(root, dir); err != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover %s: %w", entry.Name(), err))
		}
	}
	return recoveryErr
}

func recoverMetaPackTransaction(root, dir string) error {
	marker := filepath.Join(dir, metaTransactionMarker)
	data, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return os.RemoveAll(dir)
	}
	if err != nil {
		return err
	}
	var transaction metaRootTransaction
	if err := decodeStrictMetadata(data, &transaction); err != nil {
		return fmt.Errorf("decode marker: %w", err)
	}
	if err := validateMetaTransactionPaths(root, dir, transaction); err != nil {
		return err
	}
	published, err := transaction.statePublished()
	if err != nil {
		return err
	}
	if transaction.Published && published {
		return transaction.finalize()
	}
	return transaction.rollback()
}

func validateMetaTransactionPaths(root, dir string, transaction metaRootTransaction) error {
	if transaction.Format != metaTransactionFormat {
		return fmt.Errorf("meta-pack transaction format %d is unsupported", transaction.Format)
	}
	wantDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	actualDir, err := filepath.Abs(transaction.Directory)
	if err != nil || actualDir != wantDir {
		return fmt.Errorf("meta-pack transaction directory is outside its marker")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetParent, err := filepath.Abs(filepath.Dir(transaction.Target))
	if err != nil || targetParent != rootAbs {
		return fmt.Errorf("meta-pack transaction target is outside the cache")
	}
	targetKey, err := hex.DecodeString(filepath.Base(transaction.Target))
	if err != nil || len(targetKey) != sha256.Size {
		return fmt.Errorf("meta-pack transaction target is not a package cache key")
	}
	if transaction.Staged != filepath.Join(wantDir, "body") || transaction.Backup != filepath.Join(wantDir, "previous") {
		return fmt.Errorf("meta-pack transaction paths are invalid")
	}
	return nil
}

func (t metaRootTransaction) statePublished() (bool, error) {
	desired, err := os.ReadFile(t.DesiredPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, err
		}
		desired = nil
	}
	lock, err := os.ReadFile(t.LockPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, err
		}
		lock = nil
	}
	return contentHash(desired) == t.DesiredHash && contentHash(lock) == t.LockHash, nil
}

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
