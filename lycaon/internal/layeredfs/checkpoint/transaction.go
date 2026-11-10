package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

const (
	transactionVersion     = 1
	transactionJournalName = "transaction.json"

	// transactionPhasePrepared proves no target has changed, so failure only discards the record.
	transactionPhasePrepared = "prepared"
	// transactionPhaseApplying is durable before the first target mutation.
	transactionPhaseApplying = "applying"
)

// TransactionStep names a durable boundary inside a promotion.
type TransactionStep string

const (
	StepPrepared         TransactionStep = "prepared"
	StepOriginalRetained TransactionStep = "original-retained"
	StepApplying         TransactionStep = "applying"
	StepTargetMutated    TransactionStep = "target-mutated"
	StepFilesApplied     TransactionStep = "files-applied"
)

// ErrTransactionUnfinished refuses to start over a promotion recovery has not settled.
var ErrTransactionUnfinished = errors.New("checkpoint: unfinished promotion transaction requires recovery")

// TransactionConfig places one promotion's durable record. Dir belongs to this transaction alone.
type TransactionConfig struct {
	Dir               string
	ID                string
	AuthorizedGitDirs []string
	Reached           func(step TransactionStep, path string) error
	RetainLink        func(fseffect.HardLinkRequest) error
}

// Transaction is the one promotion transaction. Every target effect has a durable undo before it
// happens, a failed promotion restores the original target before it returns, and a crash leaves a
// record that recovery rolls back. Nothing waits on a retry to complete a partial promotion.
type Transaction struct {
	cfg           TransactionConfig
	dir           string
	targets       *promotionTargets
	journal       transactionJournal
	index         map[layeredfs.QualifiedPath]int
	origins       map[string]layeredfs.QualifiedPath
	slots         map[string]string
	locations     map[string]string
	keepOriginals bool
	closed        bool
}

// transactionJournal is the durable fact a restart acts on, including the absolute roots it needs.
type transactionJournal struct {
	Version      int                     `json:"version"`
	ID           string                  `json:"id"`
	Phase        string                  `json:"phase"`
	RollingBack  bool                    `json:"rolling_back,omitempty"`
	Roots        map[string]rootIdentity `json:"roots,omitempty"`
	Checkpoint   *journalCheckpoint      `json:"checkpoint,omitempty"`
	Records      []string                `json:"records,omitempty"`
	PendingSlots []string                `json:"pending_slots,omitempty"`
	Targets      []targetUndo            `json:"targets,omitempty"`
	Refs         []refUndo               `json:"refs,omitempty"`
}

type journalCheckpoint struct {
	StoreDir      string `json:"store_dir"`
	ID            string `json:"id"`
	WorkerID      string `json:"worker_id"`
	PrimaryRootID string `json:"primary_root_id,omitempty"`
}

type targetUndo struct {
	Path   layeredfs.QualifiedPath `json:"path"`
	Before targetImage             `json:"before"`
	After  targetImage             `json:"after"`
	// Prior holds After states of earlier stages this target may still be in.
	Prior []targetImage `json:"prior,omitempty"`
}

type targetImage struct {
	Kind           layeredfs.EntryKind      `json:"kind"`
	Mode           os.FileMode              `json:"mode,omitempty"`
	SHA256         string                   `json:"sha256,omitempty"`
	Size           int64                    `json:"size,omitempty"`
	LinkTarget     string                   `json:"link_target,omitempty"`
	Xattrs         map[string][]byte        `json:"xattrs,omitempty"`
	HardLinkSource *layeredfs.QualifiedPath `json:"hard_link_source,omitempty"`
	HardLinkGroup  string                   `json:"hard_link_group,omitempty"`
	RetainedSlot   string                   `json:"retained_slot,omitempty"`
	identity       string
	links          uint64
}

type refUndo struct {
	RootID string `json:"root_id,omitempty"`
	Name   string `json:"name"`
	Before string `json:"before,omitempty"`
	After  string `json:"after"`
}

// BeginTransaction durably opens a promotion. A record already in Dir is an unsettled promotion, so
// it refuses; slots left without a record carry no authority and are cleared.
func BeginTransaction(cfg TransactionConfig) (*Transaction, error) {
	if !filepath.IsAbs(cfg.Dir) || !safeComponent(cfg.ID) {
		return nil, fmt.Errorf("%w: transaction dir %q id %q", ErrInvalidManifest, cfg.Dir, cfg.ID)
	}
	if _, err := os.Lstat(filepath.Join(cfg.Dir, transactionJournalName)); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrTransactionUnfinished, cfg.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.RemoveAll(cfg.Dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, err
	}
	if err := syncDir(filepath.Dir(cfg.Dir)); err != nil {
		return nil, err
	}
	txn := newTransaction(cfg, transactionJournal{Version: transactionVersion, ID: cfg.ID, Phase: transactionPhasePrepared})
	if err := txn.saveJournal(txn.journal); err != nil {
		return nil, err
	}
	if err := txn.reach(StepPrepared, ""); err != nil {
		return nil, errors.Join(err, txn.closeRecord())
	}
	return txn, nil
}

func newTransaction(cfg TransactionConfig, journal transactionJournal) *Transaction {
	if cfg.RetainLink == nil {
		cfg.RetainLink = fseffect.HardLink
	}
	roots := make(map[string]string, len(journal.Roots))
	index := make(map[layeredfs.QualifiedPath]int, len(journal.Targets))
	for id, root := range journal.Roots {
		roots[id] = root.Path
	}
	for i, target := range journal.Targets {
		index[target.Path] = i
	}
	return &Transaction{
		cfg: cfg, dir: cfg.Dir, journal: journal, index: index,
		targets: &promotionTargets{roots: roots},
		origins: map[string]layeredfs.QualifiedPath{}, slots: map[string]string{}, locations: map[string]string{},
	}
}

// RecoverTransaction rolls back a promotion a crash left in dir. It is idempotent, and a record it
// cannot trust refuses without changing the record or any target.
func RecoverTransaction(ctx context.Context, cfg TransactionConfig) (found bool, err error) {
	txn := newTransaction(cfg, transactionJournal{})
	journal, found, err := txn.loadJournal()
	if err != nil || !found {
		return found, err
	}
	return true, newTransaction(cfg, journal).rollback(ctx)
}

// ReadTransactionID names the promotion recorded in dir, for settlement against its commit fact.
func ReadTransactionID(dir string) (string, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, transactionJournalName))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var journal transactionJournal
	if err := decodeStrict(raw, &journal); err != nil {
		return "", false, err
	}
	return journal.ID, true, nil
}

// RemoveTransactionRecord settles a promotion whose commit fact is durable elsewhere.
func RemoveTransactionRecord(dir string) error {
	if err := os.Remove(filepath.Join(dir, transactionJournalName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDirIfPresent(dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func syncDirIfPresent(dir string) error {
	if err := syncDir(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ApplyChanges applies exact changes the caller already decided, under this transaction's undo.
func (txn *Transaction) ApplyChanges(ctx context.Context, roots map[string]string, changes []PlannedChange) error {
	err := txn.applyChanges(ctx, roots, changes)
	if err != nil {
		return errors.Join(err, txn.rollback(context.WithoutCancel(ctx)))
	}
	return nil
}

func (txn *Transaction) applyChanges(ctx context.Context, roots map[string]string, changes []PlannedChange) error {
	if txn.closed {
		return errors.New("checkpoint: promotion transaction is closed")
	}
	if err := txn.addRoots(roots); err != nil {
		return err
	}
	changes = txn.canonicalChanges(changes)
	if err := txn.addTargets(changes); err != nil {
		return err
	}
	return txn.mutateTargets(ctx, changes, nil)
}

// mutateTargets makes the journal durable as applying, then mutates each target once.
func (txn *Transaction) mutateTargets(ctx context.Context, changes []PlannedChange, fault func(ApplyFaultStage) error) error {
	txn.journal.Phase = transactionPhaseApplying
	if err := txn.saveJournal(txn.journal); err != nil {
		return err
	}
	if err := txn.reach(StepApplying, ""); err != nil {
		return err
	}
	txn.targets.recordPostimage = txn.recordPostimage
	defer func() { txn.targets.recordPostimage = nil }()
	for _, change := range changes {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if err := txn.verifyRecorded(change); err != nil {
			return err
		}
		if err := txn.targets.applyPlannedChange(change); err != nil {
			return err
		}
		if err := txn.reach(StepTargetMutated, pathKey(change.Path)); err != nil {
			return err
		}
		if fault != nil {
			if err := fault(ApplyFaultMidFileApply); err != nil {
				return err
			}
		}
	}
	return context.Cause(ctx)
}

// verifyRecorded refuses a target another program changed after it was recorded. A directory an
// earlier change made implicitly has no meaningful recorded state until its own change runs.
func (txn *Transaction) verifyRecorded(change PlannedChange) error {
	if change.Action == ActionNoop || change.Action == ActionMkdir {
		return nil
	}
	current, err := txn.captureTarget(change.Path)
	if err != nil {
		return err
	}
	idx, known := txn.index[change.Path]
	if !known {
		return fmt.Errorf("checkpoint: unrecorded target %s", pathKey(change.Path))
	}
	if !txn.journal.Targets[idx].holds(current) {
		return fmt.Errorf("%w: path %s changed during promotion", ErrStalePreviewTargetChanged, pathKey(change.Path))
	}
	return nil
}

func (target targetUndo) holds(current targetImage) bool {
	if sameTarget(current, target.Before) || sameTarget(current, target.After) {
		return true
	}
	return slices.ContainsFunc(target.Prior, func(prior targetImage) bool { return sameTarget(current, prior) })
}

// JoinRecords names durable files this promotion writes, before they are written, so rollback
// removes them with the effects they describe.
func (txn *Transaction) JoinRecords(paths ...string) error {
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%w: record path %q", ErrInvalidManifest, path)
		}
		if !slices.Contains(txn.journal.Records, path) {
			txn.journal.Records = append(txn.journal.Records, path)
		}
	}
	return txn.saveJournal(txn.journal)
}

// Commit settles a promotion whose commit fact is durable, leaving its effects in place.
func (txn *Transaction) Commit() error {
	if txn.closed {
		return nil
	}
	return txn.closeRecord()
}

// Rollback restores the original target and removes this promotion's records. Rolling back a closed
// transaction does nothing, so failure paths need not know whether an apply already rolled back.
func (txn *Transaction) Rollback(ctx context.Context) error {
	if txn.closed {
		return nil
	}
	return txn.rollback(ctx)
}

func (txn *Transaction) rollback(ctx context.Context) error {
	txn.targets.recordPostimage = nil
	journal := txn.journal
	if journal.Phase == transactionPhasePrepared {
		return txn.closeRecord()
	}
	if err := txn.validateTargets(journal); err != nil {
		return err
	}
	journal.RollingBack = true
	txn.journal = journal
	if err := txn.saveJournal(journal); err != nil {
		return err
	}
	if err := txn.rollbackRefs(ctx, journal.Refs); err != nil {
		return err
	}
	if err := txn.restoreTargets(journal.Targets); err != nil {
		return err
	}
	for _, record := range journal.Records {
		if err := os.Remove(record); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := syncDirIfPresent(filepath.Dir(record)); err != nil {
			return err
		}
	}
	return txn.closeRecord()
}

// closeRecord drops the journal first: slots without a journal are inert, while a journal without
// its slots could never be recovered.
func (txn *Transaction) closeRecord() error {
	if err := RemoveTransactionRecord(txn.dir); err != nil {
		return err
	}
	txn.closed = true
	return nil
}

func (txn *Transaction) reach(step TransactionStep, path string) error {
	if txn.cfg.Reached == nil {
		return nil
	}
	return txn.cfg.Reached(step, path)
}

func (txn *Transaction) retainedDir() string   { return filepath.Join(txn.dir, "retained") }
func (txn *Transaction) postimagesDir() string { return filepath.Join(txn.dir, "postimages") }
func (txn *Transaction) originalsDir() string  { return filepath.Join(txn.dir, "originals") }

func (txn *Transaction) loadJournal() (transactionJournal, bool, error) {
	var journal transactionJournal
	raw, err := os.ReadFile(filepath.Join(txn.dir, transactionJournalName))
	if errors.Is(err, os.ErrNotExist) {
		return journal, false, nil
	}
	if err != nil {
		return journal, false, err
	}
	if err := decodeStrict(raw, &journal); err != nil {
		return journal, true, err
	}
	if err := txn.foldPostimages(&journal); err != nil {
		return journal, true, err
	}
	if err := txn.validateJournal(journal); err != nil {
		return journal, true, err
	}
	return journal, true, nil
}

func decodeStrict(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrInvalidManifest
	}
	return nil
}

func (txn *Transaction) saveJournal(journal transactionJournal) error {
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if err := writeAtomicJSONOrText(txn.dir, transactionJournalName, raw, 0o600); err != nil {
		return err
	}
	return os.RemoveAll(txn.postimagesDir())
}

func (txn *Transaction) addRoots(roots map[string]string) error {
	if !maps.Equal(txn.targets.roots, mergeRoots(txn.targets.roots, roots)) {
		for id, path := range roots {
			if existing, found := txn.targets.roots[id]; found && existing != path {
				return fmt.Errorf("%w: root %q has two targets", layeredfs.ErrInvalidRoot, id)
			}
		}
		txn.targets.roots = mergeRoots(txn.targets.roots, roots)
	}
	identities, err := txn.rootIdentities()
	if err != nil {
		return err
	}
	txn.journal.Roots = identities
	return nil
}

func mergeRoots(current, added map[string]string) map[string]string {
	out := maps.Clone(current)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, added)
	return out
}

// canonicalChanges names each target by the first root ID this transaction saw for its location,
// whatever alias a later stage uses, so a file reached under two IDs has one before-image.
func (txn *Transaction) canonicalChanges(changes []PlannedChange) []PlannedChange {
	canonical := map[string]string{}
	resolvedByRaw := make(map[string]string, len(txn.targets.roots))
	for _, id := range slices.Sorted(maps.Keys(txn.targets.roots)) {
		raw := txn.targets.roots[id]
		location, ok := resolvedByRaw[raw]
		if !ok {
			location = raw
			if resolved, err := filepath.EvalSymlinks(raw); err == nil {
				location = resolved
			}
			resolvedByRaw[raw] = location
		}
		if _, seen := txn.locations[location]; !seen {
			txn.locations[location] = id
		}
		canonical[id] = txn.locations[location]
	}
	out := slices.Clone(changes)
	for i := range out {
		if id, known := canonical[out[i].Path.RootID]; known {
			out[i].Path.RootID = id
		}
		if source := out[i].HardLinkSource; source != nil {
			named := *source
			if id, known := canonical[named.RootID]; known {
				named.RootID = id
			}
			out[i].HardLinkSource = &named
		}
	}
	return out
}
