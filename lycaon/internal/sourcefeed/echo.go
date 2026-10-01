package sourcefeed

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type hostWriteTarget struct {
	path, digest string
	temporary    bool
}

type hostWriteState struct {
	digest string
	mode   os.FileMode
	absent bool
}

type hostWriteReceipt struct {
	at        time.Time
	temporary bool
	hostWriteState
}

var (
	selfMu      sync.Mutex
	selfWrite   = map[string]hostWriteReceipt{}
	selfSweepAt time.Time
	selfTimer   *time.Timer
)

// NoteHostWrite records the observed state for watcher echo suppression.
func NoteHostWrite(absPath string) {
	noteHostWritePaths([]hostWriteTarget{{path: absPath}})
}

// NoteHostTemporaryPath suppresses lifecycle events for a write door's private
// staging or quarantine path, including its removal after commit.
func NoteHostTemporaryPath(absPath string) {
	noteHostWritePaths([]hostWriteTarget{{path: absPath, temporary: true}})
}

func noteHostWritePaths(paths []hostWriteTarget) {
	observed := make(map[string]hostWriteReceipt, len(paths))
	for _, target := range paths {
		if target.path == "" {
			continue
		}
		key, err := filepath.Abs(target.path)
		if err != nil {
			continue
		}
		state, ok := hostWriteState{}, target.temporary
		if !target.temporary {
			state, ok = readHostWriteState(key, target.digest)
		}
		if ok {
			observed[key] = hostWriteReceipt{at: time.Now(), temporary: target.temporary, hostWriteState: state}
		}
	}
	if len(observed) == 0 {
		return
	}
	now := time.Now()
	selfMu.Lock()
	defer selfMu.Unlock()
	if selfSweepAt.IsZero() || now.Sub(selfSweepAt) >= selfWriteWindow {
		pruneHostWritesLocked(now)
		selfSweepAt = now
	}
	for key, receipt := range observed {
		selfWrite[key] = receipt
	}
	if selfTimer == nil {
		selfTimer = time.AfterFunc(selfWriteWindow, expireHostWrites)
	}
}

func readHostWriteState(path, digest string) (hostWriteState, bool) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if digest != "" {
			return hostWriteState{digest: digest}, true
		}
		return hostWriteState{absent: true}, true
	}
	if err != nil || !info.Mode().IsRegular() {
		return hostWriteState{}, false
	}
	if digest == "" {
		file, err := os.Open(path) // #nosec G304 -- host-observed path; identity is checked before reading.
		if err != nil {
			return hostWriteState{}, false
		}
		defer func() { _ = file.Close() }()
		opened, err := file.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			return hostWriteState{}, false
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			return hostWriteState{}, false
		}
		digest = hex.EncodeToString(hash.Sum(nil))
	}
	return hostWriteState{digest: digest, mode: info.Mode()}, true
}

func expireHostWrites() {
	selfMu.Lock()
	defer selfMu.Unlock()
	selfTimer = nil
	now := time.Now()
	pruneHostWritesLocked(now)
	selfSweepAt = now
	if len(selfWrite) > 0 {
		selfTimer = time.AfterFunc(selfWriteWindow, expireHostWrites)
	}
}

func pruneHostWritesLocked(now time.Time) {
	cutoff := now.Add(-selfWriteWindow)
	for path, receipt := range selfWrite {
		if !receipt.at.After(cutoff) {
			delete(selfWrite, path)
		}
	}
}

func isRecentHostWrite(absPath string) bool {
	key, err := filepath.Abs(absPath)
	if err != nil {
		return false
	}
	selfMu.Lock()
	receipt, ok := selfWrite[key]
	selfMu.Unlock()
	if !ok || time.Since(receipt.at) > selfWriteWindow {
		return false
	}
	if receipt.temporary {
		return true
	}
	current, ok := readHostWriteState(key, "")
	return ok && current == receipt.hostWriteState
}
