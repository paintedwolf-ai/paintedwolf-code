package checkpointcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/pkg/api"
)

var ErrRewindPlanChanged = errors.New("rewind plan changed; review the updated preview")

func (m *Rewinds) PreviewRewind(ctx context.Context, sessionID, anchorID string) (*api.RewindPreviewResponse, error) {
	lock := m.prompt.Acquire(sessionID)
	if !lock.TryLock() {
		return nil, ErrSessionNotIdle
	}
	defer lock.Unlock()
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := m.assertRewindIdle(ctx, sess); err != nil {
		return nil, err
	}
	messages, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	anchor, ok := findMessage(messages, anchorID)
	if !ok {
		return nil, ErrRewindAnchorNotFound
	}
	if !api.IsUserIntentMessage(anchor) {
		return nil, ErrRewindAnchorIneligible
	}
	p, err := m.rewindProject(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := &api.RewindPreviewResponse{Files: []api.RewindPreviewFile{}, Issues: []api.RewindIssue{}}
	for _, msg := range messages {
		if msg.Ord >= anchor.Ord {
			out.TruncatedMessageCount++
		}
	}
	source, err := m.sourceRewinds.Prepare(ctx, p, sessionID, eligibleAnchorIDsFrom(messages, anchorID))
	if err == nil {
		err = sessioncheckpoint.CheckCoverage(ctx, m.store, p, sessiontree.RootID(ctx, m.store, sessionID), eligibleAnchorIDsFrom(messages, anchorID), source)
	}
	var blocked *sourceledger.RewindBlockedError
	if errors.As(err, &blocked) {
		for _, issue := range blocked.Issues {
			out.Issues = append(out.Issues, api.RewindIssue{RootID: issue.RootID, Path: issue.Path, Code: issue.Code})
		}
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if m.sourceRewinds.Documents != nil {
		issues, err := m.sourceRewinds.Documents.CheckSourceRewind(ctx, p, source.Selection())
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			out.Issues = append(out.Issues, api.RewindIssue{RootID: issue.RootID, Path: issue.Path, Code: issue.Code})
		}
	}
	for _, file := range source.Files {
		out.Files = append(out.Files, api.RewindPreviewFile{RootID: file.Expected.RootID, Path: file.Expected.Path, TargetPath: file.Target.Path})
	}
	if len(out.Issues) == 0 {
		out.PlanDigest, err = rewindPlanDigest(messages, source)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func rewindPlanDigest(messages []api.Message, source *sourcerewind.Operation) (string, error) {
	type row struct {
		ID  string
		Seq int64
	}
	rows := make([]row, 0, len(messages))
	for _, msg := range messages {
		rows = append(rows, row{ID: msg.ID, Seq: msg.Seq})
	}
	// Attempt and actor identities are allocated on execution, not preview.
	raw, err := json.Marshal(struct {
		Messages []row
		Files    []sourcerewind.File
	}{rows, source.Files})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
