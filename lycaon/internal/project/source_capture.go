package project

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// A capture owns the manifest and the exact bytes its fingerprint names.
type sourceRecoveryCapture struct {
	service  *SourceMutationService
	plan     *sourceMutationPlan
	writer   *sourceledger.RecoveryWriter
	digest   *sourceTreeDigest
	progress *sourceWorkProgress
	root     sourceHistoryBuffer
	rootSHA  string
}

func (s *SourceMutationService) beginRecoveryCapture(ctx context.Context, plan *sourceMutationPlan, phase string) (*sourceRecoveryCapture, error) {
	capture := &sourceRecoveryCapture{service: s, plan: plan, digest: newSourceTreeDigest(), progress: newSourceWorkProgress(ctx, phase)}
	if s.ledger != nil {
		writer, err := s.ledger.BeginRecovery(ctx, plan.ProjectID, plan.RecoveryID)
		if err != nil {
			return nil, err
		}
		capture.writer = writer
	} else {
		if s.db != nil {
			return nil, ErrSourceRecoveryFailed
		}
		s.stateMu.Lock()
		s.recoveryEntries[plan.RecoveryID] = nil
		s.stateMu.Unlock()
	}
	return capture, nil
}

func (c *sourceRecoveryCapture) close() {
	if c.writer != nil {
		c.writer.Close()
	}
}

func (c *sourceRecoveryCapture) append(ctx context.Context, entry sourceledger.RecoveryEntry, root *os.Root, path string, destination io.Writer) (sourceledger.RecoveryEntry, error) {
	writers := []io.Writer{c.progress}
	if destination != nil {
		writers = append(writers, destination)
	}
	if entry.Path == "." {
		writers = append(writers, &c.root)
	}
	sink := io.MultiWriter(writers...)
	var err error
	if c.writer != nil {
		entry, err = c.writer.Append(ctx, entry, root, path, sink)
	} else {
		entry, err = c.appendMemory(entry, root, path, sink)
	}
	if err != nil {
		return entry, err
	}
	if err := c.digest.append(entry); err != nil {
		return entry, err
	}
	if entry.Path == "." {
		c.rootSHA = entry.SHA
	}
	c.progress.entry()
	return entry, nil
}

func (c *sourceRecoveryCapture) appendMemory(entry sourceledger.RecoveryEntry, root *os.Root, path string, destination io.Writer) (sourceledger.RecoveryEntry, error) {
	if os.FileMode(entry.Mode).IsRegular() {
		content, err := root.ReadFile(path)
		if err != nil {
			return entry, err
		}
		entry.SHA = sourceblob.ContentSHA(content)
		c.service.stateMu.Lock()
		c.service.recoveryBytes[entry.SHA] = content
		c.service.stateMu.Unlock()
		if _, err := destination.Write(content); err != nil {
			return entry, err
		}
	}
	c.service.stateMu.Lock()
	c.service.recoveryEntries[c.plan.RecoveryID] = append(c.service.recoveryEntries[c.plan.RecoveryID], entry)
	c.service.stateMu.Unlock()
	return entry, nil
}

func (c *sourceRecoveryCapture) finish(ctx context.Context) error {
	plan := c.plan
	sha := c.digest.sum()
	if plan.TreeSHA != "" && plan.TreeSHA != sha {
		return ErrSourceMutationDiverged
	}
	expected := plan.AfterSHA
	if plan.Kind == "delete" {
		expected = plan.BaseSHA256
	}
	if expected != "" && expected != c.rootSHA {
		return ErrSourceMutationDiverged
	}
	if c.writer != nil {
		if err := c.writer.Flush(ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.progress.entries == 0 {
		return fmt.Errorf("%w: empty manifest", ErrSourceRecoveryFailed)
	}
	plan.TreeSHA, plan.RecoveryCount = sha, c.progress.entries
	if c.rootSHA != "" {
		if plan.Kind == "delete" {
			plan.Before, plan.BaseSHA256, plan.BeforeSize = c.root.body, c.rootSHA, c.root.size
		} else {
			plan.After, plan.AfterSHA, plan.AfterSize = c.root.body, c.rootSHA, c.root.size
		}
	}
	c.progress.report(true)
	return nil
}
