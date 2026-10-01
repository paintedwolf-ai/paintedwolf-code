// Package historyretention owns explicit policies for historical body removal.
package historyretention

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/pkg/api"
)

const PolicyFilename = "history-retention.json"

var classes = []string{"recordings", "checkpoints", "source_revisions", "scan_detail", "receipt_detail"}

var ErrInvalidPolicy = errors.New("invalid history retention policy")

func DefaultPolicy() api.HistoryRetentionPolicy {
	forever := api.HistoryRetentionRule{Mode: "forever"}
	return api.HistoryRetentionPolicy{Version: 1, Revision: 1, Recordings: forever, Checkpoints: forever, SourceRevisions: forever, ScanDetail: forever, ReceiptDetail: forever}
}

func rules(p api.HistoryRetentionPolicy) []api.HistoryRetentionRule {
	return []api.HistoryRetentionRule{p.Recordings, p.Checkpoints, p.SourceRevisions, p.ScanDetail, p.ReceiptDetail}
}

func validatePolicy(p api.HistoryRetentionPolicy) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w: %w", ErrInvalidPolicy, err)
		}
	}()
	if p.Version != 1 || p.Revision < 1 {
		return errors.New("unsupported history retention policy version or revision")
	}
	for i, rule := range rules(p) {
		switch rule.Mode {
		case "forever":
			if rule.MaxAgeDays != 0 || rule.MaxBytes != 0 {
				return fmt.Errorf("%s: keep forever has no deletion limit", classes[i])
			}
		case "max_age":
			if rule.MaxAgeDays < 1 || rule.MaxAgeDays > 365000 || rule.MaxBytes != 0 {
				return fmt.Errorf("%s: specify a positive age without a byte budget", classes[i])
			}
		case "max_bytes":
			if rule.MaxBytes < 1 || rule.MaxAgeDays != 0 {
				return fmt.Errorf("%s: specify a positive byte budget without an age", classes[i])
			}
		default:
			return fmt.Errorf("%s: unsupported retention mode", classes[i])
		}
	}
	return nil
}

func readPolicy(root string) (api.HistoryRetentionPolicy, error) {
	raw, err := os.ReadFile(filepath.Join(root, PolicyFilename))
	if errors.Is(err, os.ErrNotExist) {
		return DefaultPolicy(), nil
	}
	if err != nil {
		return api.HistoryRetentionPolicy{}, err
	}
	var policy api.HistoryRetentionPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, fmt.Errorf("%w: %w", ErrInvalidPolicy, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return policy, fmt.Errorf("%w: history retention policy must contain one JSON object", ErrInvalidPolicy)
	}
	return policy, validatePolicy(policy)
}

func writePolicy(root string, policy api.HistoryRetentionPolicy) error {
	if err := validatePolicy(policy); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: root, Rel: PolicyFilename}, Source: bytes.NewReader(raw), Mode: 0o600, DirMode: 0o700})
	return err
}

// SuspendRestoredPolicy prevents imported deletion settings from running before review.
func SuspendRestoredPolicy(root string) error {
	if _, err := os.Stat(filepath.Join(root, PolicyFilename)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	policy, err := readPolicy(root)
	if err != nil {
		return err
	}
	policy.Suspended = true
	policy.Revision++
	return writePolicy(root, policy)
}
