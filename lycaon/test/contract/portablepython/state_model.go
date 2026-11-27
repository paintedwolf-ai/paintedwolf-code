package portablepython

import (
	"fmt"
	"sort"
)

// NewReferenceModel seeds saved and editor documents at revision 1.
func NewReferenceModel(initial map[string][]byte, aliases map[string]string) *ReferenceModel {
	m := &ReferenceModel{
		Host: HostState{
			Saved:     make(map[string][]byte, len(initial)),
			Drafts:    make(map[string][]byte, len(initial)),
			Revisions: make(map[string]int, len(initial)),
			Aliases:   make(map[string]string, len(aliases)),
		},
		Guest: GuestState{
			Stage:      make(map[string][]byte),
			BaseRevs:   make(map[string]int),
			Dirty:      make(map[string]bool),
			Handles:    make(map[int]*OpenHandle),
			NextHandle: 1,
			Alive:      true,
		},
		AppliedReqs: make(map[string]bool),
	}
	for p, b := range initial {
		m.Host.Saved[p] = cloneBytes(b)
		m.Host.Drafts[p] = cloneBytes(b)
		m.Host.Revisions[p] = 1
	}
	for a, c := range aliases {
		m.Host.Aliases[a] = c
	}
	return m
}

func (m *ReferenceModel) canonical(p string) string {
	if c, ok := m.Host.Aliases[p]; ok {
		return c
	}
	return p
}

func (m *ReferenceModel) visibleBytes(canon string) ([]byte, int) {
	if b, ok := m.Guest.Stage[canon]; ok {
		return cloneBytes(b), m.Guest.BaseRevs[canon]
	}
	if b, ok := m.Host.Drafts[canon]; ok {
		return cloneBytes(b), m.Host.Revisions[canon]
	}
	if b, ok := m.Host.Saved[canon]; ok {
		return cloneBytes(b), m.Host.Revisions[canon]
	}
	return nil, 0
}

// Apply transitions the independent reference model by one ModelOp.
func (m *ReferenceModel) Apply(op ModelOp) StepResult {
	switch op.Kind {
	case OpOpen:
		return m.applyOpen(op)
	case OpRead:
		return m.applyRead(op)
	case OpWrite:
		return m.applyWrite(op)
	case OpFlush:
		return m.applyFlush(op.HandleID)
	case OpClose:
		return m.applyClose(op.HandleID)
	case OpRename:
		return m.applyRename(op.Path, op.DestPath)
	case OpHumanEdit:
		return m.applyHumanEdit(op.Path, op.Data)
	case OpApprovalDecision:
		return m.applyApproval(op.ReqID, op.Approved)
	case OpInterrupt:
		m.Guest.LatchedStop = true
		m.clearUncommitted()
		return StepResult{Reason: "interrupted"}
	case OpRestart:
		m.Guest.Alive = false
		m.clearUncommitted()
		return StepResult{Reason: "restarted"}
	default:
		return StepResult{Rejected: true, Reason: fmt.Sprintf("unknown op %q", op.Kind)}
	}
}

func (m *ReferenceModel) applyOpen(op ModelOp) StepResult {
	if !m.Guest.Alive || m.Guest.LatchedStop {
		return StepResult{Rejected: true, Reason: "guest stopped"}
	}
	canon := m.canonical(op.Path)
	id := m.Guest.NextHandle
	m.Guest.NextHandle++
	h := &OpenHandle{ID: id, Path: canon, Mode: op.Mode}
	if op.Mode == "r" {
		data, rev := m.visibleBytes(canon)
		h.Snapshot = data
		if _, captured := m.Guest.BaseRevs[canon]; !captured {
			m.Guest.BaseRevs[canon] = rev
		}
	} else if _, captured := m.Guest.BaseRevs[canon]; !captured {
		m.Guest.BaseRevs[canon] = m.Host.Revisions[canon]
	}
	m.Guest.Handles[id] = h
	return StepResult{HandleID: id}
}

func (m *ReferenceModel) applyRead(op ModelOp) StepResult {
	if !m.Guest.Alive || m.Guest.LatchedStop {
		return StepResult{Rejected: true, Reason: "guest stopped"}
	}
	if op.HandleID > 0 {
		h, ok := m.Guest.Handles[op.HandleID]
		if !ok || h.Closed {
			return StepResult{Rejected: true, Reason: "invalid handle"}
		}
		return StepResult{Data: cloneBytes(h.Snapshot)}
	}
	canon := m.canonical(op.Path)
	data, rev := m.visibleBytes(canon)
	if _, captured := m.Guest.BaseRevs[canon]; !captured {
		m.Guest.BaseRevs[canon] = rev
	}
	return StepResult{Data: data}
}

func (m *ReferenceModel) applyWrite(op ModelOp) StepResult {
	if !m.Guest.Alive || m.Guest.LatchedStop {
		return StepResult{Rejected: true, Reason: "guest stopped"}
	}
	h, ok := m.Guest.Handles[op.HandleID]
	if !ok || h.Closed || h.Mode != "w" {
		return StepResult{Rejected: true, Reason: "not a writable open handle"}
	}
	h.Buffer = append(h.Buffer, op.Data...)
	return StepResult{}
}

func (m *ReferenceModel) applyFlush(handleID int) StepResult {
	if !m.Guest.Alive || m.Guest.LatchedStop {
		return StepResult{Rejected: true, Reason: "guest stopped"}
	}
	h, ok := m.Guest.Handles[handleID]
	if !ok || h.Closed || h.Mode != "w" {
		return StepResult{Rejected: true, Reason: "not a writable open handle"}
	}
	m.Guest.Stage[h.Path] = cloneBytes(h.Buffer)
	m.Guest.Dirty[h.Path] = true
	return StepResult{}
}

func (m *ReferenceModel) applyClose(handleID int) StepResult {
	h, ok := m.Guest.Handles[handleID]
	if !ok || h.Closed {
		return StepResult{Rejected: true, Reason: "invalid handle"}
	}
	if h.Mode == "w" && m.Guest.Alive && !m.Guest.LatchedStop {
		m.Guest.Stage[h.Path] = cloneBytes(h.Buffer)
		m.Guest.Dirty[h.Path] = true
	}
	h.Closed = true
	return StepResult{}
}

func (m *ReferenceModel) applyRename(src, dst string) StepResult {
	if !m.Guest.Alive || m.Guest.LatchedStop {
		return StepResult{Rejected: true, Reason: "guest stopped"}
	}
	srcC, dstC := m.canonical(src), m.canonical(dst)
	data, rev := m.visibleBytes(srcC)
	if data == nil {
		return StepResult{Rejected: true, Reason: "source missing"}
	}
	if _, ok := m.Guest.BaseRevs[dstC]; !ok {
		m.Guest.BaseRevs[dstC] = m.Host.Revisions[dstC]
	}
	if _, ok := m.Guest.BaseRevs[srcC]; !ok {
		m.Guest.BaseRevs[srcC] = rev
	}
	m.Guest.Stage[dstC] = data
	m.Guest.Dirty[dstC] = true
	delete(m.Guest.Stage, srcC)
	delete(m.Guest.Dirty, srcC)
	return StepResult{}
}

func (m *ReferenceModel) applyHumanEdit(p string, data []byte) StepResult {
	canon := m.canonical(p)
	m.Host.Revisions[canon]++
	m.Host.Drafts[canon] = cloneBytes(data)
	return StepResult{}
}

func (m *ReferenceModel) applyApproval(reqID string, approved bool) StepResult {
	if reqID == "" {
		reqID = fmt.Sprintf("req-%d", len(m.History)+1)
	}
	if m.AppliedReqs[reqID] {
		return StepResult{Rejected: true, Reason: "duplicate approval replay"}
	}
	if !m.Guest.Alive || m.Guest.LatchedStop {
		return StepResult{Rejected: true, Reason: "stale approval after stop or restart"}
	}
	if !approved {
		m.Guest.LatchedStop = true
		m.clearUncommitted()
		return StepResult{Rejected: true, Reason: "approval declined"}
	}
	paths := m.dirtyPaths()
	for _, p := range paths {
		if m.Guest.BaseRevs[p] != m.Host.Revisions[p] {
			return StepResult{Conflict: true, Reason: fmt.Sprintf("revision conflict on %s", p)}
		}
	}
	m.AppliedReqs[reqID] = true
	for _, p := range paths {
		data := cloneBytes(m.Guest.Stage[p])
		m.Host.Revisions[p]++
		m.Host.Drafts[p] = cloneBytes(data)
		m.Host.Saved[p] = cloneBytes(data)
		m.Guest.BaseRevs[p] = m.Host.Revisions[p]
		delete(m.Guest.Dirty, p)
		m.History = append(m.History, PublishedEvent{
			ReqID:    reqID,
			Path:     p,
			Revision: m.Host.Revisions[p],
			Data:     data,
		})
	}
	return StepResult{Published: len(paths) > 0}
}

func (m *ReferenceModel) clearUncommitted() {
	for p := range m.Guest.Dirty {
		delete(m.Guest.Stage, p)
		delete(m.Guest.Dirty, p)
	}
}

func (m *ReferenceModel) dirtyPaths() []string {
	paths := make([]string, 0, len(m.Guest.Dirty))
	for p := range m.Guest.Dirty {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
