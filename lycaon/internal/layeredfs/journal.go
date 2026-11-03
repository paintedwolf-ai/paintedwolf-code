package layeredfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"time"
)

// JournalOpKind identifies a structured mutation operation in the versioned journal.
type JournalOpKind string

const (
	JournalOpCreate      JournalOpKind = "create"
	JournalOpWrite       JournalOpKind = "write"
	JournalOpTruncate    JournalOpKind = "truncate"
	JournalOpUnlink      JournalOpKind = "unlink"
	JournalOpRename      JournalOpKind = "rename"
	JournalOpRenameOver  JournalOpKind = "rename_over"
	JournalOpReplaceDir  JournalOpKind = "replace_dir"
	JournalOpMkdir       JournalOpKind = "mkdir"
	JournalOpHardLink    JournalOpKind = "hard_link"
	JournalOpSymlink     JournalOpKind = "symlink"
	JournalOpSetAttr     JournalOpKind = "setattr"
	JournalOpSetXattr    JournalOpKind = "setxattr"
	JournalOpRemoveXattr JournalOpKind = "removexattr"
)

// JournalOperation represents one structured operation recorded in the engine journal.
type JournalOperation struct {
	Seq        uint64         `json:"seq"`
	Kind       JournalOpKind  `json:"kind"`
	Path       QualifiedPath  `json:"path"`
	SourcePath *QualifiedPath `json:"source_path,omitempty"`
	NodeID     NodeID         `json:"node_id,omitempty"`
	Generation Generation     `json:"generation,omitempty"`
	EntryKind  EntryKind      `json:"entry_kind,omitempty"`
	Mode       fs.FileMode    `json:"mode,omitempty"`
	Size       int64          `json:"size,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
}

// ChangeFinalAttr carries the final state metadata and hard-link membership for a changed entry (#1658, #1663).
type ChangeFinalAttr struct {
	Mode          fs.FileMode       `json:"mode,omitempty"`
	Size          int64             `json:"size,omitempty"`
	ModTime       time.Time         `json:"mod_time,omitempty"`
	SHA256        string            `json:"sha256,omitempty"`
	LinkTarget    string            `json:"link_target,omitempty"`
	HardLinkGroup string            `json:"hard_link_group,omitempty"`
	HardLinkPaths []QualifiedPath   `json:"hard_link_paths,omitempty"`
	Xattrs        map[string][]byte `json:"xattrs,omitempty"`
}

// ChangeEntry describes the net logical or physical state of one touched path in the view.
type ChangeEntry struct {
	Path              QualifiedPath   `json:"path"`
	NodeID            NodeID          `json:"node_id,omitempty"`
	Generation        Generation      `json:"generation,omitempty"`
	Kind              EntryKind       `json:"kind"`
	PhysicalCopyUp    bool            `json:"physical_copy_up"`
	LogicalChange     bool            `json:"logical_change"`
	ContentChanged    bool            `json:"content_changed"`
	MetadataChanged   bool            `json:"metadata_changed"`
	Deleted           bool            `json:"deleted"`
	DirectoryReplaced bool            `json:"directory_replaced"`
	Final             ChangeFinalAttr `json:"final,omitempty"`
	Base              BaseCapture     `json:"base"`
}

// JournalSnapshot is the versioned serializable representation of a view's mutation journal and bases.
type JournalSnapshot struct {
	FormatVersion int                    `json:"format_version"`
	ViewID        string                 `json:"view_id"`
	Operations    []JournalOperation     `json:"operations"`
	Changes       []ChangeEntry          `json:"changes"`
	Bases         map[string]BaseCapture `json:"bases"`
}

func recordJournalLocked(c *Core, kind JournalOpKind, path QualifiedPath, srcPath *QualifiedPath, node *privateNode, size int64) {
	c.state.seq.nextSeq++
	op := JournalOperation{
		Seq:       c.state.seq.nextSeq,
		Kind:      kind,
		Path:      path,
		Size:      size,
		Timestamp: time.Now(),
	}
	if srcPath != nil {
		cp := *srcPath
		op.SourcePath = &cp
	}
	if node != nil {
		op.NodeID = node.id
		op.Generation = node.gen
		op.EntryKind = node.kind
		op.Mode = node.mode
		if size == 0 {
			op.Size = node.size
		}
	}
	c.state.journal = append(c.state.journal, op)
	c.counters.journalEntries.Add(1)
}

// JournalOperations returns a copy of all recorded structured journal operations in sequence order.
func (c *Core) JournalOperations() []JournalOperation {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]JournalOperation, len(c.state.journal))
	copy(out, c.state.journal)
	return out
}

// EnumerateChanges returns the O(changed nodes/paths) index without reading any lower directories (#1657, #1663).
func (c *Core) EnumerateChanges(logicalOnly bool) ([]ChangeEntry, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.state.released {
		return nil, ErrViewReleased
	}
	paths := collectTouchedPathsLocked(c)
	var result []ChangeEntry
	for _, qp := range paths {
		entry, include, err := buildChangeEntryLocked(c, qp, logicalOnly)
		if err != nil {
			return nil, err
		}
		if include {
			result = append(result, entry)
		}
	}
	return result, nil
}

func collectTouchedPathsLocked(c *Core) []QualifiedPath {
	touched := make(map[QualifiedPath]struct{})
	for qp := range c.state.pathIndex {
		touched[qp] = struct{}{}
	}
	for qp := range c.state.deletedPaths {
		touched[qp] = struct{}{}
	}
	paths := make([]QualifiedPath, 0, len(touched))
	for qp := range touched {
		paths = append(paths, qp)
	}
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].RootID != paths[j].RootID {
			return paths[i].RootID < paths[j].RootID
		}
		return paths[i].Rel < paths[j].Rel
	})
	return paths
}

func buildChangeEntryLocked(c *Core, qp QualifiedPath, logicalOnly bool) (ChangeEntry, bool, error) {
	base := c.state.basesByPath[qp]
	if nid, ok := c.state.pathIndex[qp]; ok {
		node := c.state.nodesByID[nid]
		if node != nil && !node.flags.unlinked {
			isLogical, contentChanged, metaChanged, sha := evaluateNodeLogicalChangeLocked(c, qp, node, base)
			if logicalOnly && !isLogical {
				return ChangeEntry{}, false, nil
			}
			var hlGroup string
			var hlPaths []QualifiedPath
			if len(node.paths) > 1 {
				hlGroup = fmt.Sprintf("node-%d-gen-%d", node.id, node.gen)
				for p := range node.paths {
					hlPaths = append(hlPaths, p)
				}
				sort.Slice(hlPaths, func(i, j int) bool {
					if hlPaths[i].RootID != hlPaths[j].RootID {
						return hlPaths[i].RootID < hlPaths[j].RootID
					}
					return hlPaths[i].Rel < hlPaths[j].Rel
				})
			}
			dirReplaced := (base.Kind == EntryDirectory && node.kind != EntryDirectory) ||
				(base.Kind != EntryAbsent && base.Kind != EntryDirectory && node.kind == EntryDirectory)
			return ChangeEntry{
				Path:              qp,
				NodeID:            node.id,
				Generation:        node.gen,
				Kind:              node.kind,
				PhysicalCopyUp:    node.flags.physicalCopyUp,
				LogicalChange:     isLogical,
				ContentChanged:    contentChanged,
				MetadataChanged:   metaChanged,
				DirectoryReplaced: dirReplaced,
				Final: ChangeFinalAttr{
					Mode:          node.mode,
					Size:          node.size,
					ModTime:       node.mtime,
					SHA256:        sha,
					LinkTarget:    node.flags.linkTarget,
					HardLinkGroup: hlGroup,
					HardLinkPaths: hlPaths,
					Xattrs:        cloneXattrs(node.xattrs),
				},
				Base: cloneBaseCapture(base),
			}, true, nil
		}
	}

	if del, isDel := c.state.deletedPaths[qp]; isDel {
		if base.Kind == EntryAbsent && logicalOnly {
			return ChangeEntry{}, false, nil
		}
		return ChangeEntry{
			Path:              qp,
			Kind:              EntryAbsent,
			LogicalChange:     base.Kind != EntryAbsent,
			Deleted:           true,
			DirectoryReplaced: del.ReplacedKind == EntryDirectory,
			Base:              cloneBaseCapture(base),
		}, true, nil
	}
	return ChangeEntry{}, false, nil
}

func evaluateNodeLogicalChangeLocked(c *Core, qp QualifiedPath, node *privateNode, base BaseCapture) (logical, contentChanged, metaChanged bool, sha string) {
	if node.kind == EntryRegular && node.flags.backingName != "" {
		sha = hashWorkingFileLocked(c, node.flags.backingName, node.flags.backingPath)
	}
	if !node.flags.physicalCopyUp || base.Kind == EntryAbsent || base.Kind != node.kind {
		return true, true, node.flags.metadataChanged, sha
	}
	if node.flags.namespaceAdded || len(node.paths) > 1 || (node.initial.originPath != nil && *node.initial.originPath != qp) {
		contentChanged = node.flags.contentChanged && (sha != node.initial.sha256 || node.size != node.initial.size)
		metaChanged = !metadataMatchesInitialLocked(node)
		return true, contentChanged, metaChanged, sha
	}
	switch node.kind {
	case EntryRegular:
		contentChanged = node.size != node.initial.size || sha != node.initial.sha256
	case EntrySymlink:
		contentChanged = node.flags.linkTarget != base.LinkTarget
	default:
		contentChanged = false
	}
	metaChanged = !metadataMatchesInitialLocked(node)
	logical = contentChanged || metaChanged
	return logical, contentChanged, metaChanged, sha
}

func hashWorkingFileLocked(c *Core, bName, bPath string) string {
	if c.backing.workDirFile == nil {
		return ""
	}
	f, err := openBackingFileRO(int(c.backing.workDirFile.Fd()), bName, bPath)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	_, _ = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

func countLogicalChangesLocked(c *Core) int {
	count := 0
	for _, qp := range collectTouchedPathsLocked(c) {
		_, include, err := buildChangeEntryLocked(c, qp, true)
		if err == nil && include {
			count++
		}
	}
	return count
}

// SnapshotJournal produces a versioned JournalSnapshot and persists it to the view's journal directory.
func (c *Core) SnapshotJournal() (JournalSnapshot, error) {
	return snapshotJournal(c)
}

func snapshotJournal(c *Core) (JournalSnapshot, error) {
	changes, err := c.EnumerateChanges(false)
	if err != nil {
		return JournalSnapshot{}, err
	}
	c.mu.RLock()
	ops := make([]JournalOperation, len(c.state.journal))
	copy(ops, c.state.journal)
	bases := make(map[string]BaseCapture, len(c.state.basesByPath))
	for qp, b := range c.state.basesByPath {
		bases[qp.RootID+":"+qp.Rel] = cloneBaseCapture(b)
	}
	journalDirFD := -1
	if c.backing.journalDirFile != nil {
		journalDirFD = int(c.backing.journalDirFile.Fd())
	}
	journalDir := c.backing.journalDir
	c.mu.RUnlock()

	snap := JournalSnapshot{
		FormatVersion: FormatVersion,
		ViewID:        c.cfg.ViewID,
		Operations:    ops,
		Changes:       changes,
		Bases:         bases,
	}
	if journalDirFD >= 0 {
		if err := persistJournalSnapshot(journalDirFD, journalDir, snap); err != nil {
			return JournalSnapshot{}, err
		}
	}
	return snap, nil
}

func persistJournalSnapshot(dirFD int, dirPath string, snap JournalSnapshot) error {
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmpName := "journal.json.tmp"
	tmpPath := filepath.Join(dirPath, tmpName)
	_ = unlinkBackingFile(dirFD, tmpName)
	f, err := createExclusiveBackingFile(dirFD, tmpName, tmpPath, 0o600)
	if err != nil {
		return err
	}
	_, wErr := io.Copy(f, bytes.NewReader(raw))
	sErr := syncFD(int(f.Fd()))
	cErr := f.Close()
	if wErr != nil || sErr != nil || cErr != nil {
		_ = unlinkBackingFile(dirFD, tmpName)
		if wErr != nil {
			return wErr
		}
		if sErr != nil {
			return sErr
		}
		return cErr
	}
	return renameBackingFile(dirFD, tmpName, "journal.json")
}

// ParseJournalSnapshot decodes and validates a persisted versioned journal payload.
func ParseJournalSnapshot(raw []byte) (JournalSnapshot, error) {
	var snap JournalSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return JournalSnapshot{}, err
	}
	if snap.FormatVersion != FormatVersion {
		return JournalSnapshot{}, fmt.Errorf("layeredfs: unsupported journal format version %d", snap.FormatVersion)
	}
	return snap, nil
}

// LoadPersistedJournal reads and validates the persisted journal from the view's backing directory.
func (c *Core) LoadPersistedJournal() (JournalSnapshot, error) {
	c.mu.RLock()
	if c.state.released || c.backing.journalDirFile == nil {
		c.mu.RUnlock()
		return JournalSnapshot{}, ErrViewReleased
	}
	dirFD := int(c.backing.journalDirFile.Fd())
	dirPath := c.backing.journalDir
	c.mu.RUnlock()

	f, err := openBackingFileRO(dirFD, "journal.json", filepath.Join(dirPath, "journal.json"))
	if err != nil {
		return JournalSnapshot{}, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(f)
	if err != nil {
		return JournalSnapshot{}, err
	}
	return ParseJournalSnapshot(raw)
}
