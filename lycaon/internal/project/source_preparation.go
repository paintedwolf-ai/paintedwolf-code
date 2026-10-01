package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

func (s *SourceMutationService) prepareSourceContents(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	if plan.Kind != "delete" || plan.RecoveryCount > 0 {
		return nil
	}
	if plan.RecoveryID == "" {
		plan.RecoveryID = row.ID
		if err := s.update(ctx, row); err != nil {
			return err
		}
	}
	if err := s.captureRecovery(ctx, plan, plan.AbsPath); err != nil {
		return err
	}
	return s.update(ctx, row)
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
