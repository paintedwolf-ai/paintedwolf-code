package projectsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

func (s *SourceEffects) applySourceCopy(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	if sourceMutationPathExists(plan.ToAbs) {
		if !plan.EffectStarted {
			return ErrSourceExists
		}
		if err := requireSourceIdentity(plan.ToAbs, plan.DestinationIdentity); err != nil {
			return err
		}
		return requireSourceFingerprint(ctx, plan.ToAbs, plan.TreeSHA)
	}
	if err := requireSourceIdentity(plan.FromAbs, plan.EntryIdentity); err != nil {
		return err
	}
	if err := s.prepareSourceCopy(ctx, row); err != nil {
		return err
	}
	var err error
	plan.DestinationIdentity, err = fspath.EntryIdentity(filepath.Join(plan.StageAbs, "entry"))
	if err != nil {
		return err
	}
	if err := s.publishSourceStage(ctx, row, filepath.FromSlash(plan.ToPath)); err != nil {
		return err
	}
	return s.removeSourceStage(plan)
}

func (s *SourceEffects) prepareSourceCopy(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	stage, err := s.openSourceStage(ctx, row, plan.ToAbs)
	if err != nil {
		return err
	}
	defer func() { _ = stage.Close() }()
	source, base, err := recoveryScope(plan.RootPath, plan.FromPath)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	transfer := &sourceTreeTransfer{ctx: ctx, digest: newSourceTreeDigest()}
	if plan.Kind == "copy" {
		plan.RecoveryID = row.ID
		if err := s.Journal.update(ctx, row); err != nil {
			return err
		}
		transfer.capture, err = s.recovery.beginRecoveryCapture(ctx, plan, "copying")
		if err != nil {
			return err
		}
		defer transfer.capture.close()
		transfer.progress = transfer.capture.progress
		transfer.digest = transfer.capture.digest
	} else {
		transfer.progress = newSourceWorkProgress(ctx, "copying")
	}
	if err := transfer.copyEntry(source, base, stage, "entry", "."); err != nil {
		return err
	}
	if plan.TreeSHA != "" && plan.TreeSHA != transfer.digest.sum() {
		return ErrSourceMutationDiverged
	}
	plan.TreeSHA = transfer.digest.sum()
	if transfer.capture != nil {
		if err := transfer.capture.finish(ctx); err != nil {
			return err
		}
	}
	transfer.progress.report(true)
	if plan.Kind == "copy" {
		if err := requireSourceFingerprint(ctx, filepath.Join(plan.StageAbs, "entry"), plan.TreeSHA); err != nil {
			return err
		}
		if err := requireSourceIdentity(plan.FromAbs, plan.EntryIdentity); err != nil {
			return err
		}
		if err := requireSourceFingerprint(ctx, plan.FromAbs, plan.TreeSHA); err != nil {
			return err
		}
	}
	return s.Journal.update(ctx, row)
}

func (s *SourceEffects) removeSourceStage(plan *sourceMutationPlan) error {
	if plan.StageAbs == "" || plan.StageIdentity == "" {
		return nil
	}
	if err := requireSourceIdentity(plan.StageAbs, plan.StageIdentity); err != nil {
		return err
	}
	// Publication leaves only an empty, exclusively created container.
	return os.Remove(plan.StageAbs)
}

func (s *SourceEffects) openSourceStage(ctx context.Context, row *sourceMutationRow, target string) (*os.Root, error) {
	plan := &row.Plan
	parent := filepath.Dir(plan.Path)
	if parent != "." {
		if err := fseffect.MkdirAll(fseffect.Location{Root: plan.RootPath, Rel: parent}, sourceCreateDirMode); err != nil {
			return nil, err
		}
	}
	if plan.StageAbs == "" {
		plan.StageAbs = filepath.Join(filepath.Dir(target), ".paintedwolf-copy-"+row.ID)
	}
	if plan.StageIdentity != "" {
		if err := clearSourceStage(plan); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		plan.StageIdentity = ""
	}
	if plan.StageIdentity == "" {
		if err := os.Mkdir(plan.StageAbs, 0o700); err != nil {
			return nil, err
		}
		identity, err := fspath.EntryIdentity(plan.StageAbs)
		if err != nil {
			return nil, err
		}
		plan.StageIdentity = identity
		if err := s.Journal.update(ctx, row); err != nil {
			return nil, err
		}
	}
	if err := requireSourceIdentity(plan.StageAbs, plan.StageIdentity); err != nil {
		return nil, err
	}
	stage, err := os.OpenRoot(plan.StageAbs)
	if err != nil {
		return nil, err
	}
	if err := requireSourceIdentity(plan.StageAbs, plan.StageIdentity); err != nil {
		_ = stage.Close()
		return nil, err
	}
	return stage, nil
}

func clearSourceStage(plan *sourceMutationPlan) error {
	rel, err := sourceRelativePath(plan.RootPath, plan.StageAbs)
	if err != nil {
		return err
	}
	return fseffect.RemoveTreeGuarded(fseffect.Location{Root: plan.RootPath, Rel: rel}, plan.StageIdentity)
}

func (s *SourceEffects) prepareSourceContents(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	if plan.Kind != "delete" || plan.RecoveryCount > 0 {
		return nil
	}
	if plan.RecoveryID == "" {
		plan.RecoveryID = row.ID
		if err := s.Journal.update(ctx, row); err != nil {
			return err
		}
	}
	if err := s.recovery.captureRecovery(ctx, plan, plan.AbsPath); err != nil {
		return err
	}
	return s.Journal.update(ctx, row)
}

type sourceEvidence struct {
	entryKind SourceEntryKind
	content   []byte
	sha256    string
	size      int64
}

func sourceHistoryEvidence(ctx context.Context, abs string) (sourceEvidence, error) {
	info, err := os.Lstat(abs)
	if err != nil {
		return sourceEvidence{}, err
	}
	if info.IsDir() {
		return sourceEvidence{entryKind: SourceEntryFolder}, nil
	}
	if !info.Mode().IsRegular() {
		return sourceEvidence{entryKind: SourceEntryFile, size: info.Size()}, nil
	}
	file, err := os.Open(abs)
	if err != nil {
		return sourceEvidence{}, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return sourceEvidence{}, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return sourceEvidence{}, ErrSourceMutationDiverged
	}
	hash := sha256.New()
	var content []byte
	reader := contextio.Reader{Context: ctx, Source: file}
	if info.Size() <= sourceledger.MaxRevisionContentBytes {
		content, err = io.ReadAll(io.TeeReader(io.LimitReader(reader, sourceledger.MaxRevisionContentBytes+1), hash))
		if err == nil && len(content) > sourceledger.MaxRevisionContentBytes {
			content = nil
			_, err = io.Copy(hash, reader)
		}
	} else {
		_, err = io.Copy(hash, reader)
	}
	if err != nil {
		return sourceEvidence{}, err
	}
	return sourceEvidence{entryKind: SourceEntryFile, content: content, sha256: hex.EncodeToString(hash.Sum(nil)), size: info.Size()}, nil
}
