package extensionstate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/fseffect"
)

// Publisher propagates committed generations to catalog consumers.
type Publisher interface {
	// InvalidateProjects drops cached project catalogs; empty id means all.
	InvalidateProjects(ctx context.Context, projectID string)
}

// Emitter announces committed extension state on the settings topic.
type Emitter interface {
	ExtensionsChanged(ctx context.Context, scope Scope)
}

// Journal makes desired and lock publication recoverable.
type Journal interface {
	Begin(ctx context.Context, op Operation) (string, error)
	FilesApplied(ctx context.Context, id string) error
	Finish(ctx context.Context, id string) error
	Recover(ctx context.Context, publisher Publisher, events Emitter) error
}

// Operation records one recoverable desired and lock publication.
type Operation struct {
	Scope           string `json:"scope"`
	ProjectID       string `json:"project_id"`
	ProjectDir      string `json:"project_dir"`
	DesiredPath     string `json:"desired_path"`
	LockPath        string `json:"lock_path"`
	PrevDesired     []byte `json:"previous_desired"`
	PrevDesiredGone bool   `json:"previous_desired_missing"`
	PrevLock        []byte `json:"previous_lock"`
	PrevLockGone    bool   `json:"previous_lock_missing"`
	NextDesired     []byte `json:"next_desired"`
	NextLock        []byte `json:"next_lock"`
}

// Owner orchestrates every extension mutation.
type Owner struct {
	Views     *catalogview.Cache
	Scanners  extpacks.ScannerRequirementChecker
	Publisher Publisher
	Events    Emitter
	Journal   Journal
}

// CurrentRevision returns the optimistic mutation token for one context.
func (o *Owner) CurrentRevision(projectDir string) (string, error) {
	snap, err := takeStateSnapshot(projectDir)
	if err != nil {
		return "", err
	}
	return snap.Revision, nil
}

type prepared struct {
	beforeCommit func(context.Context) error
	plan         *extpacks.PackagePlan
	mutate       func(extpacks.DesiredState) (extpacks.DesiredState, error)
	warnings     []string
	// cacheOnly ops change no desired/lock state.
	cacheOnly func() error
	// subjectPacks must compile; unrelated failures are isolated.
	subjectPacks []string
}

func (p *prepared) subjects() []string {
	if p == nil {
		return nil
	}
	return p.subjectPacks
}

func (p *prepared) close() {
	if p != nil {
		p.plan.Close()
	}
}

// RejectedError is a mutation whose candidate the owner refused before commit.
// Its cause is retained for host diagnostics.
type RejectedError struct{ Err error }

func (e *RejectedError) Error() string { return e.Err.Error() }

func (e *RejectedError) Unwrap() error { return e.Err }

func candidateFailure(err error) error {
	if extpacks.OperationalFailure(err) {
		return err
	}
	return &RejectedError{Err: err}
}

// Apply validates and commits one extension mutation.
func (o *Owner) Apply(ctx context.Context, intent Intent) (Result, error) {
	scope, err := intent.Scope.normalize()
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(intent.ExpectedRevision) == "" {
		return Result{}, ErrExpectedRevisionRequired
	}
	if intent.Op == nil {
		return Result{}, fmt.Errorf("extension mutation requires an operation")
	}
	if scope.Kind == "project" && !projectFileOp(intent.Op) {
		return Result{}, ErrProjectMutationUnsupported
	}
	releaseCache, err := extpacks.AcquireCacheMutationLock()
	if err != nil {
		return Result{}, err
	}
	defer releaseCache()
	if err := o.recover(ctx); err != nil {
		return Result{}, err
	}

	// Stale requests stop before downloads begin.
	snap, err := takeStateSnapshot(scope.contextDir())
	if err != nil {
		return Result{}, err
	}
	if snap.Revision != intent.ExpectedRevision {
		return Result{}, &StaleError{Current: snap.Revision}
	}

	prep, err := intent.Op.prepare(ctx, o, scope)
	if err != nil {
		return Result{}, candidateFailure(err)
	}
	defer prep.close()

	if prep.cacheOnly != nil {
		return o.applyCacheOnly(ctx, scope, intent.ExpectedRevision, prep)
	}

	// Candidate validation runs outside the publication locks.
	candDesired, candLock, err := o.candidate(scope, snap, prep)
	if err != nil {
		return Result{}, candidateFailure(err)
	}
	isolated, err := o.validateCandidate(ctx, scope, intent.Op, prep, snap, candDesired, candLock)
	if err != nil {
		return Result{}, candidateFailure(err)
	}

	if _, reviewed := intent.Op.(RemoveReviewedOp); reviewed && len(isolated) > 0 {
		return Result{}, &RejectedError{Err: fmt.Errorf("reviewed removal would disable other extensions: %s", strings.Join(isolated, "; "))}
	}

	result, err := o.commit(ctx, scope, intent.ExpectedRevision, prep)
	if err != nil {
		return Result{}, err
	}
	result.Warnings = append(result.Warnings, isolated...)
	o.publish(ctx, scope, &result)
	return result, nil
}

func projectFileOp(op Op) bool {
	switch op := op.(type) {
	case SetUnitDisabledOp:
		return true
	case UpdateUnitOp:
		return !op.OwnSet && op.Enabled != nil
	default:
		return false
	}
}

func encodeProjectSuggestion(snap stateSnapshot, cand extpacks.DesiredState) ([]byte, error) {
	sug := extpacks.EmptySuggestion()
	if !snap.ProjectDesired.Missing && len(bytes.TrimSpace(snap.ProjectDesired.Bytes)) > 0 {
		parsed, err := extpacks.ParseSuggestion(snap.ProjectDesired.Path, snap.ProjectDesired.Bytes)
		if err != nil {
			return nil, err
		}
		sug = parsed
	}
	sug.Disabled = cand.Disabled
	return extpacks.EncodeSuggestion(sug)
}

func (o *Owner) recover(ctx context.Context) error {
	if o.Journal == nil {
		return nil
	}
	return o.Journal.Recover(ctx, o.Publisher, o.Events)
}

// ClearCache deletes cached packages and republishes device state.
func (o *Owner) ClearCache(ctx context.Context, clear func() error) error {
	if clear == nil {
		return fmt.Errorf("extension cache clear function required")
	}
	releaseCache, err := extpacks.AcquireCacheMutationLock()
	if err != nil {
		return err
	}
	defer releaseCache()
	if err := o.recover(ctx); err != nil {
		return err
	}
	if err := clear(); err != nil {
		return err
	}
	return o.refreshDevice(ctx)
}

func (o *Owner) applyCacheOnly(ctx context.Context, scope Scope, expectedRevision string, prep *prepared) (Result, error) {
	release, err := extpacks.AcquireIntentLocks([]string{scope.contextDir()})
	if err != nil {
		return Result{}, err
	}
	locked := true
	defer func() {
		if locked {
			release()
		}
	}()
	snap, err := readStateSnapshot(scope.contextDir())
	if err != nil {
		return Result{}, err
	}
	if snap.Revision != expectedRevision {
		return Result{}, &StaleError{Current: snap.Revision}
	}
	if err := prep.cacheOnly(); err != nil {
		return Result{}, err
	}
	release()
	locked = false
	result := Result{Revision: snap.Revision, Warnings: prep.warnings}
	o.publish(ctx, scope, &result)
	return result, nil
}

func mutationPrep(mut extpacks.DesiredMutation) *prepared {
	return &prepared{mutate: func(d extpacks.DesiredState) (extpacks.DesiredState, error) {
		return extpacks.ApplyMutationToState(d, mut)
	}}
}

// candidate computes the mutated scope's desired/lock pair from a snapshot.
func (o *Owner) candidate(scope Scope, snap stateSnapshot, prep *prepared) (extpacks.DesiredState, extpacks.LockFile, error) {
	device, projectDesired, err := parseStates(snap)
	if err != nil {
		return extpacks.DesiredState{}, extpacks.LockFile{}, err
	}
	desired, lock := device.Desired, device.Lock
	if scope.Kind == "project" {
		desired = projectDesired
		lock = extpacks.EmptyLock()
	}
	if prep.plan != nil {
		var applyErr error
		desired, lock, applyErr = prep.plan.Apply(desired, lock)
		if applyErr != nil {
			return extpacks.DesiredState{}, extpacks.LockFile{}, applyErr
		}
	}
	if prep.mutate != nil {
		var mutErr error
		desired, mutErr = prep.mutate(desired)
		if mutErr != nil {
			return extpacks.DesiredState{}, extpacks.LockFile{}, mutErr
		}
	}
	return desired, lock, nil
}

func parseStates(snap stateSnapshot) (extpacks.DeviceState, extpacks.DesiredState, error) {
	device, err := parseDeviceState(snap.DeviceDesired, snap.DeviceLock)
	if err != nil {
		return extpacks.DeviceState{}, extpacks.DesiredState{}, err
	}
	if !snap.HasProject {
		return device, extpacks.EmptyDesired(), nil
	}
	projectDesired := extpacks.EmptyDesired()
	if !snap.ProjectDesired.Missing && len(bytes.TrimSpace(snap.ProjectDesired.Bytes)) > 0 {
		sug, parseErr := extpacks.ParseSuggestion(snap.ProjectDesired.Path, snap.ProjectDesired.Bytes)
		if parseErr != nil {
			return extpacks.DeviceState{}, extpacks.DesiredState{}, parseErr
		}
		projectDesired = extpacks.DesiredFromSuggestion(sug)
	}
	return device, projectDesired, nil
}

func parseDeviceState(desired, lock stateFile) (extpacks.DeviceState, error) {
	pair := extpacks.DeviceState{Desired: extpacks.EmptyDesired(), Lock: extpacks.EmptyLock()}
	if !desired.Missing {
		parsed, err := extpacks.ParseDesired(desired.Path, desired.Bytes)
		if err != nil {
			return extpacks.DeviceState{}, err
		}
		pair.Desired = parsed
	}
	if !lock.Missing {
		parsed, err := extpacks.ParseLock(lock.Path, lock.Bytes)
		if err != nil {
			return extpacks.DeviceState{}, err
		}
		pair.Lock = parsed
	}
	return pair, nil
}

// validateCandidate rejects subject failures and isolates unrelated failures.
func (o *Owner) validateCandidate(
	ctx context.Context,
	scope Scope,
	op Op,
	prep *prepared,
	snap stateSnapshot,
	candDesired extpacks.DesiredState,
	candLock extpacks.LockFile,
) ([]string, error) {
	subjects := prep.subjects()
	device, _, err := parseStates(snap)
	if err != nil {
		return nil, err
	}
	var projectDisabled []string
	projectID := ""
	if scope.Kind == "project" {
		projectDisabled = candDesired.Disabled
		projectID = scope.ProjectDir
	} else {
		device = extpacks.DeviceState{Desired: candDesired, Lock: candLock}
	}
	content, merged, prov, err := extpacks.DiscoverContentForState(device, projectDisabled)
	if err != nil {
		return nil, fmt.Errorf("candidate discovery: %w", err)
	}
	eff := extpacks.Resolve(ctx, extpacks.ResolveInput{
		Packs:      content,
		Desired:    merged,
		ProjectID:  projectID,
		Scanners:   o.Scanners,
		Provenance: prov,
	})
	if err := eff.BootError(); err != nil {
		return nil, fmt.Errorf("candidate rejected: %w", err)
	}
	if o.Views == nil {
		return nil, fmt.Errorf("extensionstate: view cache required for candidate validation")
	}
	_, committed, err := o.Views.ForCandidate(ctx, eff, subjects)
	if err != nil {
		return nil, fmt.Errorf("candidate rejected: %w", err)
	}
	if checked, ok := op.(catalogChecked); ok {
		if err := checked.checkAgainst(scope, eff); err != nil {
			return nil, err
		}
	}
	return isolationWarnings(eff, committed), nil
}

// isolationWarnings names packs this candidate takes out of the catalog.
func isolationWarnings(before, after *extpacks.EffectiveCatalog) []string {
	if before == after || after == nil {
		return nil
	}
	was := map[string]bool{}
	for _, p := range before.Packs {
		if p.Contributing {
			was[p.ID] = true
		}
	}
	var out []string
	for _, p := range after.Packs {
		if p.Contributing || !was[p.ID] {
			continue
		}
		reason := ""
		for _, d := range after.Diagnostics {
			if d.PackID == p.ID && d.Code == extpacks.DiagPackInvalid {
				reason = d.Message
				break
			}
		}
		if reason == "" {
			reason = fmt.Sprintf("pack %s is no longer in effect after this change", p.ID)
		}
		out = append(out, reason)
	}
	sort.Strings(out)
	return out
}

// commitPair is the encoded files one intent is about to publish.
type commitPair struct {
	desired, lock           []byte
	desiredPath, lockPath   string
	priorDesired, priorLock stateFile
}

func encodeCommitPair(scope Scope, snap stateSnapshot, candDesired extpacks.DesiredState, candLock extpacks.LockFile) (commitPair, error) {
	var pair commitPair
	var err error
	if scope.Kind == "project" {
		pair.desired, err = encodeProjectSuggestion(snap, candDesired)
		if err != nil {
			return commitPair{}, err
		}
		pair.priorDesired = snap.ProjectDesired
		pair.priorLock = stateFile{Missing: true}
	} else {
		pair.desired, err = extpacks.EncodeDesired(candDesired)
		if err != nil {
			return commitPair{}, err
		}
		pair.lock, err = extpacks.EncodeLock(candLock)
		if err != nil {
			return commitPair{}, err
		}
		pair.priorDesired = snap.DeviceDesired
		pair.priorLock = snap.DeviceLock
	}
	if scope.Kind == "project" {
		pair.desiredPath = extpacks.ProjectDesiredPath(scope.ProjectDir)
	} else {
		pair.desiredPath, err = extpacks.DeviceDesiredPath()
		if err != nil {
			return commitPair{}, err
		}
		pair.lockPath, err = extpacks.DeviceLockPath()
		if err != nil {
			return commitPair{}, err
		}
	}
	return pair, nil
}

// commit revalidates and atomically publishes the state pair.
func (o *Owner) commit(ctx context.Context, scope Scope, expectedRevision string, prep *prepared) (Result, error) {
	release, err := extpacks.AcquireIntentLocks([]string{scope.contextDir()})
	if err != nil {
		return Result{}, err
	}
	defer release()

	snap, err := readStateSnapshot(scope.contextDir())
	if err != nil {
		return Result{}, err
	}
	if snap.Revision != expectedRevision {
		return Result{}, &StaleError{Current: snap.Revision}
	}

	if prep.beforeCommit != nil {
		if err := prep.beforeCommit(ctx); err != nil {
			return Result{}, err
		}
	}

	// Publication uses the candidate derived from the locked state.
	candDesired, candLock, err := o.candidate(scope, snap, prep)
	if err != nil {
		return Result{}, err
	}
	pair, err := encodeCommitPair(scope, snap, candDesired, candLock)
	if err != nil {
		return Result{}, err
	}
	if err := prep.plan.BindCacheRoots(pair.desiredPath, pair.lockPath, pair.desired, pair.lock); err != nil {
		return Result{}, err
	}

	journalID := ""
	if o.Journal != nil {
		journalID, err = o.Journal.Begin(ctx, Operation{
			Scope:           scope.Kind,
			ProjectID:       scope.ProjectID,
			ProjectDir:      scope.ProjectDir,
			DesiredPath:     pair.desiredPath,
			LockPath:        pair.lockPath,
			PrevDesired:     pair.priorDesired.Bytes,
			PrevDesiredGone: pair.priorDesired.Missing,
			PrevLock:        pair.priorLock.Bytes,
			PrevLockGone:    pair.priorLock.Missing,
			NextDesired:     pair.desired,
			NextLock:        pair.lock,
		})
		if err != nil {
			_ = prep.plan.RollbackCacheRoots()
			return Result{}, fmt.Errorf("extension journal: %w", err)
		}
	}

	failPublication := func(cause error, finishJournal bool) (Result, error) {
		if rollbackErr := prep.plan.RollbackCacheRoots(); rollbackErr != nil {
			cause = fmt.Errorf("%w (cache rollback also failed: %w)", cause, rollbackErr)
		}
		if journalID != "" && finishJournal {
			_ = o.Journal.Finish(ctx, journalID)
		}
		return Result{}, cause
	}

	if err := prep.plan.PublishCacheRoots(); err != nil {
		return failPublication(err, true)
	}
	if scope.Kind != "project" {
		if err := writeStateFile(pair.lockPath, pair.lock, pair.priorLock); err != nil {
			return failPublication(err, false)
		}
	}
	if err := writeStateFile(pair.desiredPath, pair.desired, pair.priorDesired); err != nil {
		if scope.Kind != "project" {
			restoreErr := restoreStateFile(pair.lockPath, pair.priorLock)
			if restoreErr != nil {
				err = fmt.Errorf("%w (lock restore also failed: %w)", err, restoreErr)
			}
		}
		return failPublication(err, false)
	}
	if err := prep.plan.FinalizeCacheRoots(); err != nil {
		prep.warnings = append(prep.warnings, fmt.Sprintf("suite cache cleanup pending: %v", err))
	}

	if journalID != "" {
		journalReady := true
		if err := o.Journal.FilesApplied(ctx, journalID); err != nil {
			prep.warnings = append(prep.warnings, fmt.Sprintf("extension recovery checkpoint pending: %v", err))
			journalReady = false
		}
		if journalReady {
			if err := o.Journal.Finish(ctx, journalID); err != nil {
				prep.warnings = append(prep.warnings, fmt.Sprintf("extension commit record pending recovery: %v", err))
			}
		}
	}

	committed := snapshotAfterCommit(snap, scope.Kind, pair.desiredPath, pair.lockPath, pair.desired, pair.lock)
	result := Result{
		Revision:    committed.Revision,
		DesiredPath: pair.desiredPath,
		Desired:     candDesired,
		Warnings:    prep.warnings,
	}
	if scope.Kind == "device" {
		result.PackageChanges = lockChangesFrom(snap, candLock)
	}
	if prep.plan != nil {
		if prep.plan.Resolution.PackID != "" {
			resolution := prep.plan.Resolution
			result.Install = &resolution
			result.PackageRoot = prep.plan.PackageRoot
		}
		if prep.plan.Meta != nil {
			meta := *prep.plan.Meta
			meta.DesiredPath = pair.desiredPath
			result.Meta = &meta
		}
	}
	return result, nil
}

// publish updates consumers after commit.
func (o *Owner) publish(ctx context.Context, scope Scope, result *Result) {
	if scope.Kind == "device" {
		if err := o.refreshDevice(ctx); err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("device catalog refresh pending: %v", err))
		}
	} else {
		if o.Publisher != nil {
			o.Publisher.InvalidateProjects(ctx, scope.ProjectID)
		}
		if o.Events != nil {
			o.Events.ExtensionsChanged(ctx, scope)
		}
	}
}

func (o *Owner) refreshDevice(ctx context.Context) error {
	if o.Views == nil {
		return fmt.Errorf("extensionstate: view cache required for device refresh")
	}
	if _, _, err := PublishDeviceCatalog(ctx, o.Views, o.Scanners, nil); err != nil {
		return err
	}
	if o.Publisher != nil {
		o.Publisher.InvalidateProjects(ctx, "")
	}
	if o.Events != nil {
		o.Events.ExtensionsChanged(ctx, Scope{Kind: "device"})
	}
	return nil
}

// writeStateFile rejects out-of-band changes before atomic replacement.
func writeStateFile(path string, data []byte, prior stateFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(data),
		Mode:     0o600,
		DirMode:  0o700,
		BeforeCommit: func(_ fseffect.Target, _ fseffect.Result) error {
			current, readErr := os.ReadFile(path)
			if readErr != nil {
				if os.IsNotExist(readErr) {
					if prior.Missing {
						return nil
					}
					return fmt.Errorf("extension state %s disappeared during commit", filepath.Base(path))
				}
				return readErr
			}
			if prior.Missing || !bytes.Equal(current, prior.Bytes) {
				return fmt.Errorf("extension state %s changed outside the intent locks", filepath.Base(path))
			}
			return nil
		},
	})
	return err
}

// restoreStateFile restores captured bytes.
func restoreStateFile(path string, prior stateFile) error {
	if prior.Missing {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(prior.Bytes),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}

// lockChangesFrom reports changed device packages.
func lockChangesFrom(snap stateSnapshot, next extpacks.LockFile) []extpacks.PackageChange {
	prior := snap.DeviceLock
	current := extpacks.EmptyLock()
	if !prior.Missing {
		parsed, err := extpacks.ParseLock(prior.Path, prior.Bytes)
		if err != nil {
			// An unreadable lock reports every package as new.
			parsed = extpacks.EmptyLock()
		}
		current = parsed
	}
	changed := make([]extpacks.PackageChange, 0, len(next.Packages))
	for _, c := range extpacks.LockChanges(current, next) {
		if c.Kind != "unchanged" {
			changed = append(changed, c)
		}
	}
	return changed
}
