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

// MetadataReceipt captures stat-only file identity and change-time proof
// at the committed rename owner without reading or hashing file contents.
type MetadataReceipt struct {
	state hostWriteState
}

// CaptureMetadataReceipt records stat-only identity, mode, size, mtime, and ctime
// for absPath without opening or hashing the file. Returns (nil, false) when the
// platform cannot supply change-time proof so echo suppression fails open.
func CaptureMetadataReceipt(absPath string) (*MetadataReceipt, bool) {
	if absPath == "" {
		return nil, false
	}
	key, err := filepath.Abs(absPath)
	if err != nil {
		return nil, false
	}
	state, ok := readHostWriteState(key, "", true)
	if !ok || state.absent {
		return nil, false
	}
	return &MetadataReceipt{state: state}, true
}

type hostWriteTarget struct {
	path, digest string
	preCaptured  *hostWriteState
}

type hostWriteState struct {
	digest       string
	mode         os.FileMode
	absent       bool
	metadataOnly bool
	size         int64
	modTimeNano  int64
	changeTime   [2]int64
	info         os.FileInfo
}

func (s hostWriteState) matches(other hostWriteState) bool {
	if s.absent || other.absent {
		return s.absent && other.absent
	}
	if s.metadataOnly {
		return other.metadataOnly &&
			s.mode == other.mode &&
			s.size == other.size &&
			s.modTimeNano == other.modTimeNano &&
			s.changeTime == other.changeTime &&
			s.info != nil && other.info != nil && os.SameFile(s.info, other.info)
	}
	return !other.metadataOnly && s.digest == other.digest && s.mode == other.mode
}

type hostWriteReceipt struct {
	at time.Time
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
	if absPath == "" {
		return
	}
	noteHostWritePaths([]hostWriteTarget{{path: absPath}})
}

// NoteHostWrites records observed states for multiple paths in one batch.
func NoteHostWrites(absPaths ...string) {
	targets := make([]hostWriteTarget, 0, len(absPaths))
	for _, p := range absPaths {
		if p != "" {
			targets = append(targets, hostWriteTarget{path: p})
		}
	}
	if len(targets) > 0 {
		noteHostWritePaths(targets)
	}
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
		if target.preCaptured != nil {
			observed[key] = hostWriteReceipt{at: time.Now(), hostWriteState: *target.preCaptured}
			continue
		}
		if state, ok := readHostWriteState(key, target.digest, false); ok {
			observed[key] = hostWriteReceipt{at: time.Now(), hostWriteState: state}
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

func readHostWriteState(path, digest string, metadataOnly bool) (hostWriteState, bool) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if digest != "" {
			return hostWriteState{digest: digest}, true
		}
		return hostWriteState{absent: true}, true
	}
	if err != nil {
		return hostWriteState{}, false
	}
	if metadataOnly {
		if !info.Mode().IsRegular() && !info.IsDir() {
			return hostWriteState{}, false
		}
		ctime, ok := statChangeTime(info)
		if !ok {
			return hostWriteState{}, false
		}
		return hostWriteState{
			mode:         info.Mode(),
			metadataOnly: true,
			size:         info.Size(),
			modTimeNano:  info.ModTime().UnixNano(),
			changeTime:   ctime,
			info:         info,
		}, true
	}
	if !info.Mode().IsRegular() {
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
	current, ok := readHostWriteState(key, "", receipt.metadataOnly)
	return ok && receipt.hostWriteState.matches(current)
}
