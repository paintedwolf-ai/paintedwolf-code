package oar

// publishOccurrenceAnchor sets the paintedwolf.anchor fact for this
// occurrence: the core spelling when the local catalog id implements a core
// anchor ([OAR-PROF-1], [OAR-CONF-3]), otherwise the local id.
func publishOccurrenceAnchor(gc *GuardContext, local string, cap *EngineDeclaration) {
	if gc == nil {
		return
	}
	if local == "" {
		local = gc.Anchor
	}
	if cap == nil {
		cap = InstalledEngineDeclaration()
	}
	if IsCoreAnchor(local) {
		gc.Anchor = local
		return
	}
	if core := cap.CoreAnchorFor(local); core != "" {
		gc.Anchor = core
		return
	}
	if local != "" {
		gc.Anchor = local
	}
}

func (p *GuardPipeline) declaration() *EngineDeclaration {
	if p != nil && p.loader != nil {
		return p.loader.declaration()
	}
	return InstalledEngineDeclaration()
}
