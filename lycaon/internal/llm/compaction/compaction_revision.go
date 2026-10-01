package compaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/transcript"
)

// compactionRevision keys memoized attempts and projections to the current summary and projection semantics.
const compactionRevision = 2

func (c *SimpleCompactor) summaryRevision() (string, error) {
	if revision, ok := c.summary.(interface{ SummaryRevision() (string, error) }); ok {
		return revision.SummaryRevision()
	}
	return "local", nil
}

func (c *SimpleCompactor) sessionRevision(ctx context.Context, info SessionInfo, messages []ContextMessage, target int) (string, error) {
	prompt, err := guidance.RenderCompactionSystemPrompt(ctx, guidance.CompactionSessionSummarySystemRef)
	if err != nil {
		return "", err
	}
	record, err := renderContinuationRecord(ctx, compactionSummary{Facts: []string{"revision"}, Completed: []string{"revision"}, Pending: []string{"revision"}, Reacquire: []string{"revision"}, Constraints: []string{"revision"}}, compactedSources(messages, nil), info.RecallAvailable)
	if err != nil {
		return "", err
	}
	model, err := c.summaryRevision()
	if err != nil {
		return "", err
	}
	input, err := BuildCompactionInput(ctx, info, SliceMessages(messages, c.cfg.MessageSlice), c.cfg.SummaryInputTokens, c.cfg.SummaryMessageTokens)
	if err != nil {
		return "", err
	}
	info.Attempts, info.ChunkProjections = nil, nil
	info.CompactionGeneration = 0
	return RevisionDigest(struct {
		Version                                                        int
		Messages                                                       []ContextMessage
		Policy                                                         CompactionConfig
		Info                                                           SessionInfo
		Target                                                         int
		Prompt, Record, SummaryModel, Input, Encoding, AuthorityNotice string
	}{compactionRevision, messages, c.cfg, info, target, prompt, record, model, input.Text, modelinfo.DefaultModelContextWindows().TextEncoding(info.Model), transcript.ContentAuthorityNotice()})
}

// RevisionDigest hashes the JSON form of a revision identity.
func RevisionDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
