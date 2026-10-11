package oar

import "github.com/lycaon/lycaon/internal/oarcore"

// ObserveToolCall publishes identity and lazily fingerprints actual arguments.
func (gc *GuardContext) ObserveToolCall(name string, arguments map[string]any) {
	gc.Invocation.Tool = name
	if arguments == nil {
		arguments = map[string]any{}
	}
	gc.Invocation.ToolArgs = arguments
	gc.RegisterProvider("tool_args_fingerprint", func(gc *GuardContext) error {
		if _, explicit := gc.Published["tool_args_fingerprint"]; explicit {
			return nil
		}
		// [OAR-FACT-6] A non-tool occurrence has no call fingerprint.
		if gc.Invocation.Tool == "" {
			gc.Invocation.ToolArgsFingerprint = ""
			return nil
		}
		fingerprint, err := oarcore.ToolFingerprint(gc.Invocation.Tool, gc.Invocation.ToolArgs)
		if err != nil {
			return err
		}
		gc.Invocation.ToolArgsFingerprint = fingerprint
		return nil
	})
}

// RecordAdmittedTool retains admitted tool names in occurrence order ([OAR-PROF-9]).
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

// AdmitTool records a successful pre-invocation decision before dispatch ([OAR-PROF-9]).
func (p *GuardPipeline) AdmitTool(sessionID, tool string) {
	if p == nil || p.counters == nil || p.capability() == nil {
		return
	}
	store := p.counters
	store.mu.Lock()
	defer store.mu.Unlock()
	store.history[sessionID] = RecordAdmittedTool(store.history[sessionID], tool, p.capability().Window)
}

func (p *GuardPipeline) observeActivity(gc *GuardContext) {
	if gc.Session.RecentToolNames != nil || p.counters == nil {
		return
	}
	p.counters.mu.Lock()
	defer p.counters.mu.Unlock()
	gc.Session.RecentToolNames = append([]string{}, p.counters.history[gc.Session.SessionID]...)
}
