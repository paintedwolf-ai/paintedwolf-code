package mutation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// ManagedTempTracker binds explicitly created/published files in the session's
// managed scratch directory to their real physical identity (dev/ino) and
// declared-at-launch revision so external launches can pin them without
// classifying generic argv or retrospectively hashing replacements.
type ManagedTempTracker struct {
	mu      sync.RWMutex
	entries map[string]managedTempEntry
	aliases map[string]string
}

type managedTempEntry struct {
	abs  string
	info fs.FileInfo
	pin  toolcontract.ExecutionRevision
}

// NewManagedTempTracker creates a per-program tracker for managed temporary files.
func NewManagedTempTracker() *ManagedTempTracker {
	return &ManagedTempTracker{entries: map[string]managedTempEntry{}}
}

func (o *Owners) applyWrite(ctx context.Context, tc tools.ToolContext, input native.WriteInput) (native.WriteResult, error) {
	result, err := o.Operations.Write.Apply(ctx, input, tc)
	if err == nil && o.ManagedTemps != nil {
		o.ManagedTemps.Record(ctx, tc, o.Revisions, input.Path)
	}
	return result, err
}

// Record captures the physical identity and declared-at-launch revision of a
// managed-temp file published through the native write door.
func (t *ManagedTempTracker) Record(ctx context.Context, tc tools.ToolContext, revisions native.ExecutionRevisions, abs string) {
	if t == nil {
		return
	}
	scratch := strings.TrimSpace(tc.Host.SessionScratchDir)
	if scratch == "" || strings.TrimSpace(abs) == "" {
		return
	}
	canonicalScratch := fspath.CanonicalPath(scratch)
	canonicalAbs := fspath.CanonicalPath(abs)
	if !confine.PathAtOrUnder(canonicalAbs, canonicalScratch) || canonicalAbs == canonicalScratch {
		return
	}
	before, err := os.Lstat(canonicalAbs)
	if err != nil || !before.Mode().IsRegular() {
		return
	}
	pin, err := revisions.DeclareLaunchInput(ctx, tc, canonicalAbs)
	if err != nil {
		return
	}
	after, err := os.Lstat(canonicalAbs)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return
	}
	t.mu.Lock()
	if t.entries == nil {
		t.entries = map[string]managedTempEntry{}
	}
	if t.aliases == nil {
		t.aliases = map[string]string{}
	}
	t.entries[canonicalAbs] = managedTempEntry{abs: canonicalAbs, info: after, pin: pin}
	if cleanAbs := filepath.Clean(abs); cleanAbs != canonicalAbs {
		t.aliases[cleanAbs] = canonicalAbs
	}
	if rel, err := filepath.Rel(canonicalScratch, canonicalAbs); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		if rawAlias := filepath.Clean(filepath.Join(scratch, rel)); rawAlias != canonicalAbs {
			t.aliases[rawAlias] = canonicalAbs
		}
	}
	t.mu.Unlock()
}

func (t *ManagedTempTracker) lookupLocked(candidate string) (managedTempEntry, bool) {
	cleaned := filepath.Clean(candidate)
	if entry, ok := t.entries[cleaned]; ok {
		return entry, true
	}
	if canonical, ok := t.aliases[cleaned]; ok {
		entry, ok := t.entries[canonical]
		return entry, ok
	}
	// Resolve only the parent directory so a symlinked, dangling, or removed
	// leaf still maps back to the tracked managed-temp entry instead of
	// dropping out as an untracked operand.
	parent := fspath.CanonicalPath(filepath.Dir(cleaned))
	parentKey := filepath.Join(parent, filepath.Base(cleaned))
	if entry, ok := t.entries[parentKey]; ok {
		return entry, true
	}
	return managedTempEntry{}, false
}

func (t *ManagedTempTracker) resolve(scratchDir, cwd, arg string) (managedTempEntry, bool) {
	if t == nil || arg == "" {
		return managedTempEntry{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.entries) == 0 {
		return managedTempEntry{}, false
	}
	if filepath.IsAbs(arg) {
		if entry, ok := t.lookupLocked(arg); ok {
			return entry, true
		}
		if entry, ok := t.entries[fspath.CanonicalPath(arg)]; ok {
			return entry, true
		}
		return managedTempEntry{}, false
	}
	scratch := strings.TrimSpace(scratchDir)
	if scratch == "" || cwd == "" {
		return managedTempEntry{}, false
	}
	if entry, ok := t.lookupLocked(filepath.Join(cwd, arg)); ok {
		return entry, true
	}
	canonicalCwd := fspath.CanonicalPath(cwd)
	if !confine.PathAtOrUnder(canonicalCwd, fspath.CanonicalPath(scratch)) {
		return managedTempEntry{}, false
	}
	return t.lookupLocked(filepath.Join(canonicalCwd, arg))
}

// DeclareLaunchInputs verifies and records declared_at_launch revision pins
// for any launch operands that explicitly resolve to tracked managed-temp
// files, rejecting physical replacement before the external process starts.
func (o *Owners) DeclareLaunchInputs(ctx context.Context, tc tools.ToolContext, executionID, cwd string, argv []string) error {
	if o == nil || o.ManagedTemps == nil || o.Pins == nil || executionID == "" || len(argv) < 2 {
		return nil
	}
	var seen map[string]bool
	for _, arg := range argv[1:] {
		entry, ok := o.ManagedTemps.resolve(tc.Host.SessionScratchDir, cwd, arg)
		if !ok {
			continue
		}
		if seen == nil {
			seen = map[string]bool{}
		}
		if seen[entry.abs] {
			continue
		}
		seen[entry.abs] = true
		if err := verifyManagedTempEntry(entry); err != nil {
			return err
		}
		if err := o.Pins.AppendRevisionPin(ctx, executionID, entry.pin); err != nil {
			return err
		}
	}
	return nil
}

func verifyManagedTempEntry(entry managedTempEntry) error {
	conflict := func(reason string) error {
		return &toolrejection.ToolReject{
			Code: "COMMAND_REVISION_CONFLICT",
			Data: map[string]any{
				"path":              entry.abs,
				"expected_revision": entry.pin.Identity(),
				"reason":            reason,
			},
		}
	}
	lstatInfo, err := os.Lstat(entry.abs)
	if err != nil || !lstatInfo.Mode().IsRegular() || !os.SameFile(entry.info, lstatInfo) {
		return conflict("managed temporary input physical identity changed before launch")
	}
	f, err := fseffect.OpenRead(fseffect.Location{Root: filepath.Dir(entry.abs), Rel: filepath.Base(entry.abs)})
	if err != nil {
		return conflict("managed temporary input unavailable at launch")
	}
	defer func() { _ = f.Close() }()
	fdInfo, err := f.Stat()
	if err != nil || !fdInfo.Mode().IsRegular() || !os.SameFile(entry.info, fdInfo) || !os.SameFile(lstatInfo, fdInfo) {
		return conflict("managed temporary input physical identity changed before launch")
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return conflict("managed temporary input unreadable at launch")
	}
	if size != entry.pin.ContentBytes || hex.EncodeToString(h.Sum(nil)) != entry.pin.DiskSHA256 {
		return conflict("managed temporary input bytes changed before launch")
	}
	return nil
}
