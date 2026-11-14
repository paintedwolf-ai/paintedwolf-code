package oar

// ObserveToolCall publishes the call's identity and arguments.
func (gc *GuardContext) ObserveToolCall(name string, arguments map[string]any) {
	gc.Invocation.Tool = name
	if gc.Published == nil {
		gc.Published = map[string]any{}
	}
	gc.Published["paintedwolf.native_operation"] = ""
	gc.Invocation.ToolArgs = arguments
}

// RecordAdmittedTool retains admitted tool names in occurrence order ([OAR-FACT-10]).
func RecordAdmittedTool(history []string, name string, window int) []string {
	if window <= 0 {
		return nil
	}
	history = append(history, name)
	if len(history) > window {
		history = append([]string(nil), history[len(history)-window:]...)
	}
	return history
}

// AdmitTool records a successful pre-invocation decision before dispatch ([OAR-FACT-10]).
func (p *GuardPipeline) AdmitTool(sessionID, tool string) {
	if p == nil || p.counters == nil || p.declaration() == nil {
		return
	}
	store := p.counters
	store.mu.Lock()
	defer store.mu.Unlock()
	store.history[sessionID] = RecordAdmittedTool(store.history[sessionID], tool, p.declaration().Window)
}

func (p *GuardPipeline) observeActivity(gc *GuardContext) {
	if gc.Session.RecentToolNames != nil || p.counters == nil {
		return
	}
	p.counters.mu.Lock()
	defer p.counters.mu.Unlock()
	gc.Session.RecentToolNames = append([]string{}, p.counters.history[gc.Session.SessionID]...)
}
