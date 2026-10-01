package hitl

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"github.com/lycaon/lycaon/pkg/api"
)

const contentApplyPlanVersion = 1

// ContentApplyPlan is the immutable host-defined edit reviewed by a content_apply
// checkpoint. The client can select hunk ids, but only this plan can turn those
// ids into bytes.
type ContentApplyPlan struct {
	Version    int                    `json:"version"`
	ID         string                 `json:"id"`
	Tool       string                 `json:"tool"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	Path       string                 `json:"path"`
	Before     *string                `json:"before,omitempty"`
	After      string                 `json:"after"`
	Hunks      []ContentApplyPlanHunk `json:"hunks"`
}

// ContentApplyPlanHunk is one host-authored selectable edit. Start and End are
// byte offsets into the plan's before content and never cross another hunk.
type ContentApplyPlanHunk struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

// CompileContentApplyPlan freezes proposed bytes into independently selectable
// host hunks. Adjacent changed lines stay together; a complete unchanged line
// starts a new selectable hunk.
func CompileContentApplyPlan(payload ContentApplyPayload) (*ContentApplyPlan, error) {
	if strings.TrimSpace(payload.Tool) == "" || strings.TrimSpace(payload.Path) == "" {
		return nil, fmt.Errorf("content_apply tool and path are required")
	}
	before := derefContent(payload.Before)
	hunks, err := compileContentApplyHunks(payload.Path, before, payload.After)
	if err != nil {
		return nil, err
	}
	plan := ContentApplyPlan{
		Version: contentApplyPlanVersion,
		Tool:    payload.Tool, ToolCallID: payload.ToolCallID, Path: payload.Path,
		Before: cloneStringPointer(payload.Before), After: payload.After,
		Hunks: hunks,
	}
	id, err := plan.canonicalID()
	if err != nil {
		return nil, err
	}
	plan.ID = id
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return &plan, nil
}

func compileContentApplyHunks(path, before, after string) ([]ContentApplyPlanHunk, error) {
	edits := udiff.Strings(before, after)
	if len(edits) == 0 {
		return []ContentApplyPlanHunk{newContentApplyHunk(path, 0, 0, "", "", 0)}, nil
	}
	groups := make([][]udiff.Edit, 0, len(edits))
	group := []udiff.Edit{edits[0]}
	for _, edit := range edits[1:] {
		previous := group[len(group)-1]
		if strings.Count(before[previous.End:edit.Start], "\n") >= 2 {
			groups = append(groups, group)
			group = []udiff.Edit{edit}
			continue
		}
		group = append(group, edit)
	}
	groups = append(groups, group)

	hunks := make([]ContentApplyPlanHunk, 0, len(groups))
	for index, edits := range groups {
		start := edits[0].Start
		end := edits[len(edits)-1].End
		relative := make([]udiff.Edit, 0, len(edits))
		for _, edit := range edits {
			relative = append(relative, udiff.Edit{Start: edit.Start - start, End: edit.End - start, New: edit.New})
		}
		replacement, err := udiff.Apply(before[start:end], relative)
		if err != nil {
			return nil, fmt.Errorf("compile content_apply hunk: %w", err)
		}
		hunks = append(hunks, newContentApplyHunk(path, start, end, before[start:end], replacement, index))
	}
	return hunks, nil
}

func newContentApplyHunk(path string, start, end int, before, after string, index int) ContentApplyPlanHunk {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%d\x00%d\x00%s\x00%s", index, path, start, end, before, after)))
	return ContentApplyPlanHunk{
		ID: "hunk_" + hex.EncodeToString(sum[:8]), Path: path,
		Before: before, After: after, Start: start, End: end,
	}
}

// Validate proves that the immutable hunk set reconstructs the proposed after
// bytes and that every selectable id names exactly one non-overlapping edit.
func (p ContentApplyPlan) Validate() error {
	if p.Version != contentApplyPlanVersion || strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Tool) == "" || strings.TrimSpace(p.Path) == "" {
		return fmt.Errorf("content_apply plan identity is incomplete")
	}
	if len(p.Hunks) == 0 {
		return fmt.Errorf("content_apply plan has no hunks")
	}
	before := derefContent(p.Before)
	edits := make([]udiff.Edit, 0, len(p.Hunks))
	seen := make(map[string]struct{}, len(p.Hunks))
	lastEnd := 0
	for _, hunk := range p.Hunks {
		if strings.TrimSpace(hunk.ID) == "" || hunk.Path != p.Path || hunk.Start < lastEnd || hunk.Start < 0 || hunk.End < hunk.Start || hunk.End > len(before) {
			return fmt.Errorf("content_apply plan hunk is invalid")
		}
		if _, ok := seen[hunk.ID]; ok {
			return fmt.Errorf("content_apply plan has duplicate hunk %q", hunk.ID)
		}
		seen[hunk.ID] = struct{}{}
		if hunk.Before != before[hunk.Start:hunk.End] {
			return fmt.Errorf("content_apply plan hunk %q does not match before content", hunk.ID)
		}
		edits = append(edits, udiff.Edit{Start: hunk.Start, End: hunk.End, New: hunk.After})
		lastEnd = hunk.End
	}
	composed, err := udiff.Apply(before, edits)
	if err != nil || composed != p.After {
		return fmt.Errorf("content_apply plan hunks do not reconstruct proposed content")
	}
	expectedID, err := p.canonicalID()
	if err != nil {
		return err
	}
	if p.ID != expectedID {
		return fmt.Errorf("content_apply plan content does not match its identity")
	}
	return nil
}

// Compose returns bytes authored only from the immutable plan. approve selects
// every hunk; approve_partial selects the exact host ids supplied by the client.
func (p ContentApplyPlan) Compose(decision api.ContentApplyDecision, approvedHunks []string) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if decision == api.ContentApplyApprove {
		if len(approvedHunks) != 0 {
			return "", ErrContentApplySelectionInvalid
		}
		return p.After, nil
	}
	if decision != api.ContentApplyApprovePartial || len(approvedHunks) == 0 {
		return "", ErrContentApplySelectionInvalid
	}
	selected := make(map[string]struct{}, len(approvedHunks))
	for _, id := range approvedHunks {
		id = strings.TrimSpace(id)
		if id == "" {
			return "", ErrContentApplySelectionInvalid
		}
		if _, duplicate := selected[id]; duplicate {
			return "", ErrContentApplySelectionInvalid
		}
		selected[id] = struct{}{}
	}
	edits := make([]udiff.Edit, 0, len(selected))
	for _, hunk := range p.Hunks {
		if _, ok := selected[hunk.ID]; !ok {
			continue
		}
		delete(selected, hunk.ID)
		edits = append(edits, udiff.Edit{Start: hunk.Start, End: hunk.End, New: hunk.After})
	}
	if len(selected) != 0 {
		return "", ErrContentApplyHunkNotFound
	}
	composed, err := udiff.Apply(derefContent(p.Before), edits)
	if err != nil {
		return "", fmt.Errorf("compose content_apply plan: %w", err)
	}
	return composed, nil
}

func (p ContentApplyPlan) WireHunks() []api.ContentApplyHunk {
	out := make([]api.ContentApplyHunk, 0, len(p.Hunks))
	for _, hunk := range p.Hunks {
		out = append(out, api.ContentApplyHunk{ID: hunk.ID, Path: hunk.Path, Before: hunk.Before, After: hunk.After})
	}
	return out
}

func (p ContentApplyPlan) canonicalID() (string, error) {
	copy := p
	copy.ID = ""
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("marshal content_apply plan: %w", err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func storeContentApplyPlan(plan *ContentApplyPlan) (map[string]any, error) {
	if plan == nil {
		return nil, fmt.Errorf("content_apply plan required")
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("store content_apply plan: %w", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("store content_apply plan: %w", err)
	}
	return stored, nil
}

func contentApplyPlanFromMap(raw map[string]any) (*ContentApplyPlan, error) {
	if raw == nil {
		return nil, fmt.Errorf("content_apply plan missing")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("read content_apply plan: %w", err)
	}
	var plan ContentApplyPlan
	if err := json.Unmarshal(encoded, &plan); err != nil {
		return nil, fmt.Errorf("read content_apply plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return &plan, nil
}

func derefContent(content *string) string {
	if content == nil {
		return ""
	}
	return *content
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
