package harnessfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// PreludeStep names a real tool invocation and its required boundary result.
type PreludeStep struct {
	CheckpointStatus api.CheckpointStatus  `json:"checkpoint_status,omitempty" yaml:"checkpoint_status,omitempty"`
	ID               string                `json:"id" yaml:"id"`
	Tool             string                `json:"tool" yaml:"tool"`
	Args             map[string]any        `json:"args" yaml:"args"`
	Outcome          api.ToolResultOutcome `json:"outcome" yaml:"outcome"`
	Code             string                `json:"code,omitempty" yaml:"code,omitempty"`
	JSON             map[string]any        `json:"json,omitempty" yaml:"json,omitempty"`
}

type Prelude struct {
	Final       string        `json:"final,omitempty" yaml:"final,omitempty"`
	OperationID string        `json:"operation_id" yaml:"operation_id"`
	Steps       []PreludeStep `json:"steps" yaml:"steps"`
}

type PreludeReceipt struct {
	OperationID   string    `json:"operation_id"`
	SessionID     string    `json:"session_id"`
	ToolCallIDs   []string  `json:"tool_call_ids"`
	EntryAt       time.Time `json:"entry_at"`
	TranscriptSeq int64     `json:"transcript_seq"`
}

type PreludeMessages interface {
	GetMessages(context.Context, string) ([]api.Message, error)
}

type PreludeController struct {
	root     string
	messages PreludeMessages
	mu       sync.Mutex
}

func NewPreludeController(root string, messages PreludeMessages) (*PreludeController, error) {
	if !configdir.IsHarnessChannel() || messages == nil {
		return nil, fmt.Errorf("conversation preparation requires an isolated harness and transcript store")
	}
	root = filepath.Join(root, "conversation-preparation")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &PreludeController{root: root, messages: messages}, nil
}

func (p Prelude) Validate() error {
	if _, err := uuid.Parse(p.OperationID); err != nil {
		return fmt.Errorf("preparation operation_id must be a UUID")
	}
	if len(p.Steps) < 1 || len(p.Steps) > 16 {
		return fmt.Errorf("preparation requires 1 to 16 steps")
	}
	seen := map[string]bool{}
	for _, step := range p.Steps {
		if step.ID == "" || seen[step.ID] || step.Tool == "" || step.Args == nil {
			return fmt.Errorf("preparation requires unique step ids, tools, and arguments")
		}
		if step.Outcome != api.ToolResultOutcomeCompleted && step.Outcome != api.ToolResultOutcomeRejected && step.Outcome != api.ToolResultOutcomeError {
			return fmt.Errorf("preparation step must require a completed, rejected, or error result")
		}
		if step.CheckpointStatus != "" && step.CheckpointStatus != api.CheckpointStatusApproved && step.CheckpointStatus != api.CheckpointStatusRejected {
			return fmt.Errorf("preparation checkpoint must require an approved or rejected decision")
		}
		seen[step.ID] = true
	}
	return nil
}

func (p Prelude) CallID(step PreludeStep) string {
	return uuid.NewSHA1(uuid.MustParse(p.OperationID), []byte(step.ID)).String()
}

func (c *PreludeController) Install(ctx context.Context, sessionID string, plan Prelude) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if _, err := uuid.Parse(sessionID); err != nil {
		return fmt.Errorf("preparation session_id must be a UUID")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	path := sessionID + ".json"
	var existing Prelude
	if err := c.read(path, &existing); err == nil {
		if !sameJSON(existing, plan) {
			return fmt.Errorf("preparation identity changed")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	rows, err := c.messages.GetMessages(ctx, sessionID)
	if err != nil {
		return err
	}
	if hasConversation(rows) {
		return fmt.Errorf("install preparation before submitting the first user request")
	}
	return c.write(path, plan)
}

func (c *PreludeController) Receipt(sessionID string) (PreludeReceipt, error) {
	if _, err := uuid.Parse(sessionID); err != nil {
		return PreludeReceipt{}, err
	}
	var receipt PreludeReceipt
	err := c.read(sessionID+".entry.json", &receipt)
	return receipt, err
}

func (c *PreludeController) read(name string, value any) error {
	body, err := os.ReadFile(filepath.Join(c.root, name))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, value)
}

func (c *PreludeController) write(name string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: c.root, Rel: name}, Source: bytes.NewReader(body), Mode: 0o600})
	return err
}

func (c *PreludeController) Wrap(inner modelcall.LLMClient) modelcall.LLMClient {
	return &preludeClient{controller: c, inner: inner}
}

type preludeClient struct {
	controller *PreludeController
	inner      modelcall.LLMClient
}

func (p *preludeClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	stream, err := p.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	result, _, err := modelcall.CollectStream(stream)
	return result, err
}

func (p *preludeClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	call, err := p.controller.next(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("fixture preparation: %w", err)
	}
	if call == nil {
		final, err := p.controller.priorHandoff(ctx, req.Debug.SessionID)
		if err != nil {
			return nil, err
		}
		if final == "" {
			return p.inner.Stream(ctx, req)
		}
		out := make(chan modelcall.StreamChunk, 1)
		out <- modelcall.StreamChunk{Content: final, ProviderID: "fixture", Model: "preparation", Scripted: true, Usage: modelcall.TokenUsage{Present: true}, Done: true}
		close(out)
		return out, nil
	}
	if err := offeredPreparationCall(req, *call); err != nil {
		return nil, err
	}
	out := make(chan modelcall.StreamChunk, 1)
	out <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{*call}, ProviderID: "fixture", Model: "preparation", Scripted: true, Usage: modelcall.TokenUsage{Present: true}, Done: true}
	close(out)
	return out, nil
}

func (c *PreludeController) next(ctx context.Context, req modelcall.CompletionRequest) (*api.ToolCall, error) {
	if req.Debug.SessionID == "" {
		return nil, nil
	}
	if _, err := uuid.Parse(req.Debug.SessionID); err != nil {
		return nil, fmt.Errorf("invalid fixture session ID: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var plan Prelude
	if err := c.read(req.Debug.SessionID+".json", &plan); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if _, err := c.Receipt(req.Debug.SessionID); err == nil {
		return nil, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	rows, err := c.messages.GetMessages(ctx, req.Debug.SessionID)
	if err != nil {
		return nil, err
	}
	receipt := PreludeReceipt{OperationID: plan.OperationID, SessionID: req.Debug.SessionID}
	for _, row := range rows {
		receipt.TranscriptSeq = max(receipt.TranscriptSeq, row.Seq)
	}
	for _, step := range plan.Steps {
		id := plan.CallID(step)
		result, started := preludeResult(rows, id)
		if result == nil {
			if started {
				return nil, fmt.Errorf("preparation step %s has no durable result", step.ID)
			}
			return &api.ToolCall{ID: id, Name: step.Tool, Args: step.Args}, nil
		}
		if err := matchPreludeResult(step, result); err != nil {
			return nil, err
		}
		receipt.ToolCallIDs = append(receipt.ToolCallIDs, id)
	}
	receipt.EntryAt = time.Now().UTC()
	if plan.Final != "" && userBoundaries(rows) < 2 {
		return nil, nil
	}
	return nil, c.write(req.Debug.SessionID+".entry.json", receipt)
}

func (c *PreludeController) priorHandoff(ctx context.Context, sessionID string) (string, error) {
	if sessionID == "" {
		return "", nil
	}
	if _, err := uuid.Parse(sessionID); err != nil {
		return "", fmt.Errorf("invalid fixture session ID: %w", err)
	}
	var plan Prelude
	if err := c.read(sessionID+".json", &plan); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if plan.Final == "" {
		return "", nil
	}
	rows, err := c.messages.GetMessages(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if userBoundaries(rows) < 2 {
		return plan.Final, nil
	}
	return "", nil
}

func userBoundaries(rows []api.Message) int {
	count := 0
	for _, row := range rows {
		if api.IsUserIntentMessage(row) {
			count++
		}
	}
	return count
}

func preludeResult(rows []api.Message, id string) (*api.ToolResult, bool) {
	started := false
	var result *api.ToolResult
	for _, row := range rows {
		for _, call := range row.ToolCalls {
			if call.ID == id {
				started = true
			}
		}
		if row.ToolResult != nil && row.ToolResult.ToolCallID == id {
			result = row.ToolResult
		}
	}
	return result, started
}

func matchPreludeResult(step PreludeStep, result *api.ToolResult) error {
	if result.Outcome != step.Outcome || result.Process != nil {
		return fmt.Errorf("preparation step %s did not reach its required terminal outcome", step.ID)
	}
	if step.CheckpointStatus != "" && (result.CheckpointDecision == nil || result.CheckpointDecision.Status != step.CheckpointStatus) {
		return fmt.Errorf("preparation step %s has no matching checkpoint decision", step.ID)
	}
	if step.Code != "" {
		found := false
		for _, code := range result.Codes {
			found = found || code == step.Code
		}
		if !found {
			return fmt.Errorf("preparation step %s has a different result code", step.ID)
		}
	}
	if len(step.JSON) != 0 {
		var actual map[string]any
		if err := json.Unmarshal([]byte(result.Content), &actual); err != nil {
			return fmt.Errorf("preparation step %s did not return its declared JSON envelope", step.ID)
		}
		for key, expected := range step.JSON {
			if !sameJSON(actual[key], expected) {
				return fmt.Errorf("preparation step %s field %s differs", step.ID, key)
			}
		}
	}
	return nil
}

func hasConversation(rows []api.Message) bool {
	for _, row := range rows {
		if row.Role == api.MessageRoleUser || row.Role == api.MessageRoleAssistant || row.ToolResult != nil || len(row.ToolCalls) != 0 {
			return true
		}
	}
	return false
}

func sameJSON(a, b any) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func offeredPreparationCall(req modelcall.CompletionRequest, call api.ToolCall) error {
	for _, meta := range req.Tools {
		if meta.Name == call.Name {
			return tools.ValidateToolArgs(meta.ArgsSchema, call.Args)
		}
	}
	return fmt.Errorf("preparation tool %s is not offered on surface %s", call.Name, req.Debug.Surface)
}
