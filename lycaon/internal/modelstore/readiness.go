package modelstore

// Status reports readiness and any required repair action for (model, device).
func (s *Store) Status(model, device string) Readiness {
	records, err := s.readRecords()
	if err != nil {
		return Readiness{
			State:  StateRepairRequired,
			Repair: RepairRecoverJournal,
			Reason: err.Error(),
		}
	}
	key := model + "/" + device
	var current, prev *Manifest
	for i := range records {
		m := records[i]
		if m.Model+"/"+m.Device != key {
			continue
		}
		if current != nil && current.Revision != m.Revision {
			cp := *current
			prev = &cp
		}
		cp := m
		current = &cp
	}
	if current == nil {
		return Readiness{
			State:  StateAbsent,
			Repair: RepairImportSeed,
			Reason: "model revision not activated",
		}
	}
	if _, err := s.checkRevision(*current); err != nil {
		return Readiness{State: StateRepairRequired, Active: current, Previous: prev,
			Repair: RepairRestageArtifact, Reason: err.Error()}
	}
	return Readiness{
		State:    StateReady,
		Active:   current,
		Previous: prev,
		Repair:   RepairNone,
	}
}
