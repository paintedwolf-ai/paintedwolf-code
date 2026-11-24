package progress

import (
	"slices"
	"strconv"
	"strings"
)

type rowEditOutcome struct {
	rows       []Row
	nextSeq    uint64
	addedIDs   []string
	removedIDs []string
}

func applyRowMutation(before Snapshot, m Mutation, basis PreparedProgressBasis) (rowEditOutcome, error) {
	seq := before.NextItemSeq()
	if !m.HasRowEdits() {
		if basis.HasBasis && basis.CheckDocumentVersion && before.DocumentVersion() != basis.ExpectedDocumentVersion {
			return rowEditOutcome{}, &MutationRejection{
				Code: "PROGRESS_STALE_BASIS",
				Data: map[string]any{"changed_ids": []string{}},
			}
		}
		return rowEditOutcome{
			rows:       slices.Clone(before.Rows),
			nextSeq:    seq,
			addedIDs:   []string{},
			removedIDs: []string{},
		}, nil
	}
	if err := checkTargetsAndBasis(before, m, basis); err != nil {
		return rowEditOutcome{}, err
	}

	var after []Row
	addedIDs := []string{}
	removedIDs := []string{}

	if len(m.Replace) > 0 {
		for _, prev := range before.Rows {
			removedIDs = append(removedIDs, prev.ID)
		}
		after = make([]Row, len(m.Replace))
		for i, spec := range m.Replace {
			id := "p" + strconv.FormatUint(seq, 10)
			seq++
			state := strings.TrimSpace(spec.State)
			if state == "" {
				state = ProgressStatePending
			}
			after[i] = Row{ID: id, State: state, Label: strings.TrimSpace(spec.Label)}
			addedIDs = append(addedIDs, id)
		}
	} else {
		removeSet := make(map[string]struct{}, len(m.Remove))
		for _, id := range m.Remove {
			removeSet[id] = struct{}{}
		}
		updateMap := make(map[string]UpdateRowSpec, len(m.Update))
		for _, u := range m.Update {
			updateMap[u.ID] = u
		}

		surviving := make([]Row, 0, len(before.Rows))
		for _, r := range before.Rows {
			if _, drop := removeSet[r.ID]; drop {
				removedIDs = append(removedIDs, r.ID)
				continue
			}
			if u, ok := updateMap[r.ID]; ok {
				if u.Label != nil {
					r.Label = strings.TrimSpace(*u.Label)
				}
				if u.State != nil {
					r.State = strings.TrimSpace(*u.State)
				}
			}
			surviving = append(surviving, r)
		}

		var atStart []Row
		var defaultTail []Row
		afterAnchor := make(map[string][]Row)
		for _, spec := range m.Add {
			id := "p" + strconv.FormatUint(seq, 10)
			seq++
			state := strings.TrimSpace(spec.State)
			if state == "" {
				state = ProgressStatePending
			}
			row := Row{ID: id, State: state, Label: strings.TrimSpace(spec.Label)}
			addedIDs = append(addedIDs, id)
			switch spec.AfterID {
			case "start":
				atStart = append(atStart, row)
			case "":
				defaultTail = append(defaultTail, row)
			default:
				afterAnchor[spec.AfterID] = append(afterAnchor[spec.AfterID], row)
			}
		}

		tailIdx := trailingOptionalIndex(surviving)
		after = make([]Row, 0, len(surviving)+len(m.Add))
		after = append(after, atStart...)
		for i, r := range surviving {
			if i == tailIdx {
				after = append(after, defaultTail...)
			}
			after = append(after, r)
			if group := afterAnchor[r.ID]; len(group) > 0 {
				after = append(after, group...)
			}
		}
		if tailIdx == len(surviving) {
			after = append(after, defaultTail...)
		}
	}

	if len(after) > MaxAuthorProgressRows {
		return rowEditOutcome{}, &MutationRejection{
			Code: "PROGRESS_TOO_MANY_ROWS",
			Data: map[string]any{"row_count": len(after), "max_rows": MaxAuthorProgressRows},
		}
	}
	if len(before.Rows) == 0 && len(after) > 0 && OpenStepCount(after) == 0 {
		return rowEditOutcome{}, &MutationRejection{
			Code: "PROGRESS_NO_OPEN_STEP",
			Data: map[string]any{},
		}
	}
	if label, found := FirstPendingAfterOptional(after); found {
		return rowEditOutcome{}, &MutationRejection{
			Code: "PROGRESS_PENDING_AFTER_OPTIONAL",
			Data: map[string]any{"label": PreviewLabel(label)},
		}
	}
	return rowEditOutcome{
		rows:       after,
		nextSeq:    seq,
		addedIDs:   addedIDs,
		removedIDs: removedIDs,
	}, nil
}

func checkTargetsAndBasis(before Snapshot, m Mutation, basis PreparedProgressBasis) error {
	byID := make(map[string]Row, len(before.Rows))
	for _, r := range before.Rows {
		byID[r.ID] = r
	}
	refs := referencedRowIDs(m)
	var unknown []string
	for _, id := range refs {
		if _, ok := byID[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return &MutationRejection{
			Code: "PROGRESS_ITEM_NOT_FOUND",
			Data: map[string]any{"unknown_ids": unknown},
		}
	}
	if !basis.HasBasis {
		return nil
	}
	if basis.CheckDocumentVersion && before.DocumentVersion() != basis.ExpectedDocumentVersion {
		return &MutationRejection{
			Code: "PROGRESS_STALE_BASIS",
			Data: map[string]any{"changed_ids": []string{}},
		}
	}
	var changed []string
	for _, id := range refs {
		if expected, ok := basis.ExpectedLabels[id]; ok && byID[id].Label != expected {
			changed = append(changed, id)
		}
	}
	if len(changed) > 0 {
		return &MutationRejection{
			Code: "PROGRESS_STALE_BASIS",
			Data: map[string]any{"changed_ids": changed},
		}
	}
	return nil
}

func referencedRowIDs(m Mutation) []string {
	seen := make(map[string]struct{})
	var out []string
	push := func(id string) {
		if id == "" || id == "start" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range m.Remove {
		push(id)
	}
	for _, u := range m.Update {
		push(u.ID)
	}
	for _, a := range m.Add {
		push(a.AfterID)
	}
	return out
}

func trailingOptionalIndex(rows []Row) int {
	idx := len(rows)
	for idx > 0 && rows[idx-1].State == ProgressStateOptional {
		idx--
	}
	return idx
}
