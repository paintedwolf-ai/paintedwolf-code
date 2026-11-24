package progress

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const maxResultHandleRunes = 128

// RowSpec defines one row in a replace operation.
type RowSpec struct {
	Label string `json:"label"`
	State string `json:"state"`
}

// AddRowSpec defines one row in an add operation.
type AddRowSpec struct {
	Label   string `json:"label"`
	State   string `json:"state"`
	AfterID string `json:"after_id,omitempty"`
}

// UpdateRowSpec patches an existing row by ID.
type UpdateRowSpec struct {
	ID    string  `json:"id"`
	State *string `json:"state,omitempty"`
	Label *string `json:"label,omitempty"`
}

// Mutation represents a validated row-DML and handle-acknowledgement request.
type Mutation struct {
	Replace            []RowSpec
	Add                []AddRowSpec
	Update             []UpdateRowSpec
	Remove             []string
	AcknowledgeResults []string
}

// HasRowEdits reports whether the mutation modifies progress rows.
func (m Mutation) HasRowEdits() bool {
	return len(m.Replace) > 0 || len(m.Add) > 0 || len(m.Update) > 0 || len(m.Remove) > 0
}

// IsEmpty reports whether no operations were requested.
func (m Mutation) IsEmpty() bool {
	return !m.HasRowEdits() && len(m.AcknowledgeResults) == 0
}

// MutationRejection carries a catalog rejection code and structured facts.
type MutationRejection struct {
	Code string
	Data map[string]any
}

func (e *MutationRejection) Error() string { return e.Code }

// MutationResult records committed row changes and handle acknowledgement.
type MutationResult struct {
	ReconciliationStatus string   `json:"reconciliation_status"`
	Updated              bool     `json:"document_changed"`
	StateChanged         bool     `json:"state_changed"`
	AddedItemIDs         []string `json:"added_item_ids"`
	ClosedItemIDs        []string `json:"closed_item_ids"`
	RemovedItemIDs       []string `json:"removed_item_ids"`
	Snapshot             Snapshot `json:"snapshot"`
}

// AdvancesWorkflow reports whether deliverable state advanced or results were acknowledged.
func (r MutationResult) AdvancesWorkflow() bool {
	return r.StateChanged || r.ReconciliationStatus == "acknowledged"
}

// PreparedProgressBasis binds a held program envelope to the snapshot observed at preparation.
type PreparedProgressBasis struct {
	HasBasis                bool
	CheckDocumentVersion    bool
	ExpectedDocumentVersion uint64
	ExpectedLabels          map[string]string
}

// CaptureBasis snapshots host preconditions for a prepared mutation that may wait before applying.
func CaptureBasis(m Mutation, snap Snapshot) PreparedProgressBasis {
	basis := PreparedProgressBasis{HasBasis: true}
	if len(m.Replace) > 0 {
		basis.CheckDocumentVersion = true
		basis.ExpectedDocumentVersion = snap.DocumentVersion()
		return basis
	}
	byID := make(map[string]string, len(snap.Rows))
	for _, r := range snap.Rows {
		byID[r.ID] = r.Label
	}
	labels := make(map[string]string)
	record := func(id string) {
		if label, ok := byID[id]; ok {
			labels[id] = label
		}
	}
	for _, u := range m.Update {
		record(u.ID)
	}
	for _, id := range m.Remove {
		record(id)
	}
	for _, a := range m.Add {
		if a.AfterID != "" && a.AfterID != "start" {
			record(a.AfterID)
		}
	}
	if len(labels) > 0 {
		basis.ExpectedLabels = labels
	}
	return basis
}

// PrepareMutation parses and validates update_progress arguments before persistence.
func PrepareMutation(args map[string]any) (Mutation, error) {
	if len(args) == 0 {
		return Mutation{}, fmt.Errorf("at least one operation required")
	}
	var m Mutation
	var err error
	if raw, ok := args["replace"]; ok && raw != nil {
		if m.Replace, err = parseReplaceSpecs(raw); err != nil {
			return Mutation{}, err
		}
	}
	if raw, ok := args["add"]; ok && raw != nil {
		if m.Add, err = parseAddSpecs(raw); err != nil {
			return Mutation{}, err
		}
	}
	if raw, ok := args["update"]; ok && raw != nil {
		if m.Update, err = parseUpdateSpecs(raw); err != nil {
			return Mutation{}, err
		}
	}
	if raw, ok := args["remove"]; ok && raw != nil {
		if m.Remove, err = parseRemoveIDs(raw); err != nil {
			return Mutation{}, err
		}
	}
	if raw, ok := args["acknowledge_results"]; ok && raw != nil {
		if m.AcknowledgeResults, err = parseAckHandles(raw); err != nil {
			return Mutation{}, err
		}
	}
	if err := ValidateMutation(m); err != nil {
		return Mutation{}, err
	}
	return m, nil
}

// ValidateMutation checks structural and intra-call conflict invariants on m.
func ValidateMutation(m Mutation) error {
	if m.IsEmpty() {
		return fmt.Errorf("at least one operation required")
	}
	if len(m.Replace) > 0 && (len(m.Add) > 0 || len(m.Update) > 0 || len(m.Remove) > 0) {
		return &MutationRejection{
			Code: "PROGRESS_CONFLICTING_OPERATIONS",
			Data: map[string]any{"reason": "replace_with_incremental_edits"},
		}
	}
	if len(m.Replace) > MaxAuthorProgressRows {
		return &MutationRejection{
			Code: "PROGRESS_TOO_MANY_ROWS",
			Data: map[string]any{"row_count": len(m.Replace), "max_rows": MaxAuthorProgressRows},
		}
	}
	for i := range m.Replace {
		if _, err := cleanAuthorLabel(m.Replace[i].Label); err != nil {
			return err
		}
		state := strings.TrimSpace(m.Replace[i].State)
		if state == "" {
			state = ProgressStatePending
		}
		if !isValidState(state) {
			return fmt.Errorf("invalid replace[%d].state %q", i, state)
		}
	}
	removed := make(map[string]struct{}, len(m.Remove))
	for _, id := range m.Remove {
		if !IsValidRowID(id) {
			return fmt.Errorf("invalid remove id %q", id)
		}
		if _, dup := removed[id]; dup {
			return &MutationRejection{
				Code: "PROGRESS_CONFLICTING_OPERATIONS",
				Data: map[string]any{"reason": "duplicate_target_id", "id": id},
			}
		}
		removed[id] = struct{}{}
	}
	updated := make(map[string]struct{}, len(m.Update))
	for i, u := range m.Update {
		if !IsValidRowID(u.ID) {
			return fmt.Errorf("invalid update[%d].id %q", i, u.ID)
		}
		if _, dup := updated[u.ID]; dup {
			return &MutationRejection{
				Code: "PROGRESS_CONFLICTING_OPERATIONS",
				Data: map[string]any{"reason": "duplicate_target_id", "id": u.ID},
			}
		}
		if _, clash := removed[u.ID]; clash {
			return &MutationRejection{
				Code: "PROGRESS_CONFLICTING_OPERATIONS",
				Data: map[string]any{"reason": "update_and_remove_same_id", "id": u.ID},
			}
		}
		if u.State == nil && u.Label == nil {
			return &MutationRejection{
				Code: "PROGRESS_CONFLICTING_OPERATIONS",
				Data: map[string]any{"reason": "empty_update", "id": u.ID},
			}
		}
		if u.State != nil && !isValidState(*u.State) {
			return fmt.Errorf("invalid update[%d].state %q", i, *u.State)
		}
		if u.Label != nil {
			if _, err := cleanAuthorLabel(*u.Label); err != nil {
				return err
			}
		}
		updated[u.ID] = struct{}{}
	}
	for i, a := range m.Add {
		if _, err := cleanAuthorLabel(a.Label); err != nil {
			return err
		}
		state := strings.TrimSpace(a.State)
		if state == "" {
			state = ProgressStatePending
		}
		if state != ProgressStatePending && state != ProgressStateOptional {
			return fmt.Errorf("invalid add[%d].state %q", i, state)
		}
		if a.AfterID != "" && a.AfterID != "start" {
			if !IsValidRowID(a.AfterID) {
				return fmt.Errorf("invalid add[%d].after_id %q", i, a.AfterID)
			}
			if _, clash := removed[a.AfterID]; clash {
				return &MutationRejection{
					Code: "PROGRESS_CONFLICTING_OPERATIONS",
					Data: map[string]any{"reason": "anchor_removed_in_same_call", "id": a.AfterID},
				}
			}
		}
	}
	for i, h := range m.AcknowledgeResults {
		trimmed := strings.TrimSpace(h)
		if trimmed == "" || utf8.RuneCountInString(trimmed) > maxResultHandleRunes {
			return fmt.Errorf("invalid acknowledge_results[%d]", i)
		}
	}
	return nil
}

func cleanAuthorLabel(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	if label == "" {
		return "", fmt.Errorf("row label required")
	}
	if utf8.RuneCountInString(label) > MaxAuthorLabelRunes {
		return "", fmt.Errorf("row label exceeds %d characters", MaxAuthorLabelRunes)
	}
	return label, nil
}

func parseReplaceSpecs(raw any) ([]RowSpec, error) {
	if typed, ok := raw.([]RowSpec); ok {
		out := slices.Clone(typed)
		for i := range out {
			out[i].Label = strings.TrimSpace(out[i].Label)
			if strings.TrimSpace(out[i].State) == "" {
				out[i].State = ProgressStatePending
			}
		}
		return out, nil
	}
	items, ok := toObjectSlice(raw)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("replace must be a non-empty array")
	}
	out := make([]RowSpec, len(items))
	for i, item := range items {
		label, ok := item["label"].(string)
		if !ok {
			return nil, fmt.Errorf("replace[%d].label required", i)
		}
		state := ProgressStatePending
		if s, ok := item["state"].(string); ok && strings.TrimSpace(s) != "" {
			state = strings.TrimSpace(s)
		}
		out[i] = RowSpec{Label: strings.TrimSpace(label), State: state}
	}
	return out, nil
}

func parseAddSpecs(raw any) ([]AddRowSpec, error) {
	if typed, ok := raw.([]AddRowSpec); ok {
		out := slices.Clone(typed)
		for i := range out {
			out[i].Label = strings.TrimSpace(out[i].Label)
			out[i].AfterID = strings.TrimSpace(out[i].AfterID)
			if strings.TrimSpace(out[i].State) == "" {
				out[i].State = ProgressStatePending
			}
		}
		return out, nil
	}
	items, ok := toObjectSlice(raw)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("add must be a non-empty array")
	}
	out := make([]AddRowSpec, len(items))
	for i, item := range items {
		label, ok := item["label"].(string)
		if !ok {
			return nil, fmt.Errorf("add[%d].label required", i)
		}
		state := ProgressStatePending
		if s, ok := item["state"].(string); ok && strings.TrimSpace(s) != "" {
			state = strings.TrimSpace(s)
		}
		afterID, _ := item["after_id"].(string)
		out[i] = AddRowSpec{
			Label:   strings.TrimSpace(label),
			State:   state,
			AfterID: strings.TrimSpace(afterID),
		}
	}
	return out, nil
}

func parseUpdateSpecs(raw any) ([]UpdateRowSpec, error) {
	if typed, ok := raw.([]UpdateRowSpec); ok {
		out := make([]UpdateRowSpec, len(typed))
		for i, u := range typed {
			u.ID = strings.TrimSpace(u.ID)
			if u.State != nil {
				s := strings.TrimSpace(*u.State)
				u.State = &s
			}
			if u.Label != nil {
				l := strings.TrimSpace(*u.Label)
				u.Label = &l
			}
			out[i] = u
		}
		return out, nil
	}
	items, ok := toObjectSlice(raw)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("update must be a non-empty array")
	}
	out := make([]UpdateRowSpec, len(items))
	for i, item := range items {
		id, ok := item["id"].(string)
		if !ok {
			return nil, fmt.Errorf("update[%d].id required", i)
		}
		spec := UpdateRowSpec{ID: strings.TrimSpace(id)}
		if rawState, present := item["state"]; present && rawState != nil {
			s, ok := rawState.(string)
			if !ok {
				return nil, fmt.Errorf("update[%d].state must be a string", i)
			}
			trimmed := strings.TrimSpace(s)
			spec.State = &trimmed
		}
		if rawLabel, present := item["label"]; present && rawLabel != nil {
			l, ok := rawLabel.(string)
			if !ok {
				return nil, fmt.Errorf("update[%d].label must be a string", i)
			}
			trimmed := strings.TrimSpace(l)
			spec.Label = &trimmed
		}
		out[i] = spec
	}
	return out, nil
}

func parseRemoveIDs(raw any) ([]string, error) {
	strs, ok := toStringSlice(raw)
	if !ok || len(strs) == 0 {
		return nil, fmt.Errorf("remove must be a non-empty string array")
	}
	out := make([]string, len(strs))
	for i, s := range strs {
		out[i] = strings.TrimSpace(s)
	}
	return out, nil
}

func parseAckHandles(raw any) ([]string, error) {
	strs, ok := toStringSlice(raw)
	if !ok || len(strs) == 0 {
		return nil, fmt.Errorf("acknowledge_results must be a non-empty string array")
	}
	seen := make(map[string]struct{}, len(strs))
	out := make([]string, 0, len(strs))
	for _, s := range strs {
		trimmed := strings.TrimSpace(s)
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out, nil
}

func toObjectSlice(raw any) ([]map[string]any, bool) {
	switch v := raw.(type) {
	case []map[string]any:
		return v, true
	case []any:
		out := make([]map[string]any, len(v))
		for i, elem := range v {
			m, ok := elem.(map[string]any)
			if !ok {
				return nil, false
			}
			out[i] = m
		}
		return out, true
	default:
		return nil, false
	}
}

func toStringSlice(raw any) ([]string, bool) {
	switch v := raw.(type) {
	case []string:
		return v, true
	case []any:
		out := make([]string, len(v))
		for i, elem := range v {
			s, ok := elem.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	default:
		return nil, false
	}
}
