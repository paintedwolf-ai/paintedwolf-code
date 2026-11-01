package toolcontract

// ProgramStateCapacity is an admission snapshot; reservations remain authoritative.
type ProgramStateCapacity struct{ Maximum, Held, Available int64 }

func (m *ProgramMemory) BatchStateCapacity() ProgramStateCapacity {
	m.mu.Lock()
	defer m.mu.Unlock()
	return ProgramStateCapacity{Maximum: m.limits.State, Held: m.state, Available: max(0, m.limits.State-m.state)}
}
