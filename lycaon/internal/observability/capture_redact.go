package observability

import (
	"strconv"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

// CaptureRedactor applies the runtime secret catalog to captured text.
type CaptureRedactor func(string) string

var captureRedactor atomic.Pointer[CaptureRedactor]

// SetCaptureRedactor sets runtime catalog redaction.
func SetCaptureRedactor(fn CaptureRedactor) {
	if fn == nil {
		captureRedactor.Store(nil)
		return
	}
	captureRedactor.Store(&fn)
}

// RedactCaptureText strips secrets from captured text.
func RedactCaptureText(s string) string {
	var fn CaptureRedactor
	if loaded := captureRedactor.Load(); loaded != nil {
		fn = *loaded
	}
	return redactCaptureText(s, fn)
}

func redactCaptureText(s string, catalogRedactor CaptureRedactor) string {
	if s == "" {
		return s
	}
	s = redactCaptureFloor(s)
	if catalogRedactor != nil {
		return catalogRedactor(s)
	}
	return s
}

// redactCaptureFloor covers named fields before catalog screening.
func redactCaptureFloor(s string) string {
	s = RedactString(s)
	return jsonSecretFieldRE.ReplaceAllString(s, `"$1":"`+redacted+`"`)
}

// RedactCaptureJSON structurally scrubs a complete body before catalog screening.
func RedactCaptureJSON(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	return RedactCaptureText(string(ScrubJSON(raw)))
}

// RedactCaptureValue scrubs a structured capture value.
func RedactCaptureValue(value any) any {
	return redactCaptureValue(ScrubValue(value))
}

func redactCaptureValue(value any) any {
	switch typed := value.(type) {
	case string:
		return RedactCaptureText(typed)
	case []string:
		out := make([]string, len(typed))
		for i := range typed {
			out[i] = RedactCaptureText(typed[i])
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = redactCaptureValue(typed[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			redactedKey := RedactCaptureText(key)
			for suffix := 2; ; suffix++ {
				if _, exists := out[redactedKey]; !exists {
					break
				}
				redactedKey = RedactCaptureText(key) + "#" + strconv.Itoa(suffix)
			}
			out[redactedKey] = redactCaptureValue(item)
		}
		return out
	default:
		return value
	}
}

func redactCaptureStrings(values []string) []string {
	if values == nil {
		return nil
	}
	out := append([]string(nil), values...)
	for i := range out {
		out[i] = RedactCaptureText(out[i])
	}
	return out
}

func redactCaptureGrounding(source *api.CitationGrounding) *api.CitationGrounding {
	if source == nil {
		return nil
	}
	out := *source
	out.ObservedPathsSample = redactCaptureStrings(source.ObservedPathsSample)
	out.ObservedURLsSample = redactCaptureStrings(source.ObservedURLsSample)
	out.CitedURLs = redactCaptureStrings(source.CitedURLs)
	out.ProseLeaksSample = redactCaptureStrings(source.ProseLeaksSample)
	out.ProseAdvisoriesSample = redactCaptureStrings(source.ProseAdvisoriesSample)
	out.Checks = append([]api.CitationGroundingCheck(nil), source.Checks...)
	for i := range out.Checks {
		out.Checks[i].Label = RedactCaptureText(out.Checks[i].Label)
		out.Checks[i].Summary = RedactCaptureText(out.Checks[i].Summary)
		out.Checks[i].Matched = redactCaptureStrings(out.Checks[i].Matched)
		out.Checks[i].Failed = redactCaptureStrings(out.Checks[i].Failed)
	}
	out.Findings = append([]api.CitationGroundingFinding(nil), source.Findings...)
	for i := range out.Findings {
		out.Findings[i].Path = RedactCaptureText(out.Findings[i].Path)
		out.Findings[i].Excerpt = RedactCaptureText(out.Findings[i].Excerpt)
		out.Findings[i].Note = RedactCaptureText(out.Findings[i].Note)
	}
	out.CitedEvidence = append([]api.CitationGroundingCitedEvidence(nil), source.CitedEvidence...)
	for i := range out.CitedEvidence {
		out.CitedEvidence[i].Path = RedactCaptureText(out.CitedEvidence[i].Path)
		out.CitedEvidence[i].Excerpt = RedactCaptureText(out.CitedEvidence[i].Excerpt)
	}
	out.EvidenceRecords = append([]api.CitationGroundingEvidenceRecord(nil), source.EvidenceRecords...)
	for i := range out.EvidenceRecords {
		out.EvidenceRecords[i].Path = RedactCaptureText(out.EvidenceRecords[i].Path)
		out.EvidenceRecords[i].URLs = redactCaptureStrings(out.EvidenceRecords[i].URLs)
		out.EvidenceRecords[i].Excerpt = RedactCaptureText(out.EvidenceRecords[i].Excerpt)
	}
	return &out
}

// RedactMessagesForCapture copies messages and scrubs captured content.
func RedactMessagesForCapture(msgs []api.Message) []api.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := messageview.RedactMessages(msgs)
	for i := range out {
		out[i] = redactMessageContent(out[i])
	}
	return out
}

func redactCaptureToolResult(source *api.ToolResult) *api.ToolResult {
	if source == nil {
		return nil
	}
	result := *source
	result.Content = RedactCaptureText(result.Content)
	if args, ok := RedactCaptureValue(result.ToolArgs).(map[string]any); ok {
		result.ToolArgs = args
	}
	result.Feedback = append([]api.ToolFeedback(nil), result.Feedback...)
	for i := range result.Feedback {
		if details, ok := RedactCaptureValue(result.Feedback[i].Details).(map[string]any); ok {
			result.Feedback[i].Details = details
		}
		if result.Feedback[i].Subject != nil {
			subject := *result.Feedback[i].Subject
			subject.ID = RedactCaptureText(subject.ID)
			result.Feedback[i].Subject = &subject
		}
	}
	if result.Invocation != nil {
		invocation := *result.Invocation
		invocation.Evidence.Ref = RedactCaptureText(invocation.Evidence.Ref)
		invocation.Evidence.OwnerRef = RedactCaptureText(invocation.Evidence.OwnerRef)
		if invocation.Failure != nil {
			failure := *invocation.Failure
			failure.OwnerRef = RedactCaptureText(failure.OwnerRef)
			if details, ok := RedactCaptureValue(failure.Details).(map[string]any); ok {
				failure.Details = details
			}
			invocation.Failure = &failure
		}
		result.Invocation = &invocation
	}
	if result.Visual != nil {
		visual := *result.Visual
		visual.Caption = RedactCaptureText(visual.Caption)
		result.Visual = &visual
	}
	if result.FileEdit != nil {
		edit := *result.FileEdit
		edit.Path = RedactCaptureText(edit.Path)
		edit.After = RedactCaptureText(edit.After)
		if edit.Before != nil {
			before := RedactCaptureText(*edit.Before)
			edit.Before = &before
		}
		result.FileEdit = &edit
	}
	if result.CheckpointDecision != nil {
		decision := *result.CheckpointDecision
		decision.Tool = RedactCaptureText(decision.Tool)
		decision.Subject = RedactCaptureText(decision.Subject)
		decision.CausingCommand = RedactCaptureText(decision.CausingCommand)
		decision.Location = RedactCaptureText(decision.Location)
		decision.Guidance = RedactCaptureText(decision.Guidance)
		decision.GrantTitle = RedactCaptureText(decision.GrantTitle)
		result.CheckpointDecision = &decision
	}
	result.ExternalAccess = redactCaptureExternalAccess(result.ExternalAccess)
	if result.Skill != nil {
		skill := *result.Skill
		skill.Description = RedactCaptureText(skill.Description)
		skill.Instructions = RedactCaptureText(skill.Instructions)
		skill.Dir = RedactCaptureText(skill.Dir)
		skill.Resources = redactCaptureStrings(skill.Resources)
		skill.License = RedactCaptureText(skill.License)
		skill.Compatibility = RedactCaptureText(skill.Compatibility)
		skill.AllowedTools = RedactCaptureText(skill.AllowedTools)
		result.Skill = &skill
	}
	if result.Verdict != nil {
		verdict := *result.Verdict
		verdict.EvidenceKey = RedactCaptureText(verdict.EvidenceKey)
		verdict.Phase = RedactCaptureText(verdict.Phase)
		verdict.Grounding = redactCaptureGrounding(verdict.Grounding)
		result.Verdict = &verdict
	}
	return &result
}

func redactCaptureExternalAccess(source *api.ExternalAccess) *api.ExternalAccess {
	if source == nil {
		return nil
	}
	out := *source
	out.DeclaredDestinations = redactCaptureStrings(out.DeclaredDestinations)
	out.Endpoints = append([]api.ExternalAccessEndpoint(nil), out.Endpoints...)
	for i := range out.Endpoints {
		out.Endpoints[i].Host = RedactCaptureText(out.Endpoints[i].Host)
	}
	out.Sockets = append([]api.ExternalAccessSocket(nil), out.Sockets...)
	for i := range out.Sockets {
		out.Sockets[i].ApprovedPath = RedactCaptureText(out.Sockets[i].ApprovedPath)
		out.Sockets[i].ResolvedPath = RedactCaptureText(out.Sockets[i].ResolvedPath)
	}
	out.Detections = append([]api.ExternalAccessDetection(nil), out.Detections...)
	for i := range out.Detections {
		out.Detections[i].RuleTitle = RedactCaptureText(out.Detections[i].RuleTitle)
	}
	return &out
}

// redactMessageContent copies and scrubs one message.
func redactMessageContent(msg api.Message) api.Message {
	msg.Content = RedactCaptureText(msg.Content)

	if len(msg.ContentParts) > 0 {
		parts := make([]api.MessageContentPart, len(msg.ContentParts))
		copy(parts, msg.ContentParts)
		for i := range parts {
			parts[i].Content = RedactCaptureText(parts[i].Content)
			parts[i].Path = RedactCaptureText(parts[i].Path)
			parts[i].SourceRef = RedactCaptureText(parts[i].SourceRef)
		}
		msg.ContentParts = parts
	}
	if msg.WorkflowBoundary != nil {
		boundary := *msg.WorkflowBoundary
		boundary.Phase = RedactCaptureText(boundary.Phase)
		boundary.Reason = RedactCaptureText(boundary.Reason)
		msg.WorkflowBoundary = &boundary
	}
	if msg.ProgressComplete != nil {
		progress := *msg.ProgressComplete
		progress.Steps = append([]api.ProgressStep(nil), progress.Steps...)
		for i := range progress.Steps {
			progress.Steps[i].Label = RedactCaptureText(progress.Steps[i].Label)
		}
		msg.ProgressComplete = &progress
	}
	if msg.ProgressUpdate != nil {
		progress := *msg.ProgressUpdate
		progress.Steps = append([]api.ProgressStep(nil), progress.Steps...)
		for i := range progress.Steps {
			progress.Steps[i].Label = RedactCaptureText(progress.Steps[i].Label)
		}
		progress.Changes = append([]api.ProgressChange(nil), progress.Changes...)
		for i := range progress.Changes {
			progress.Changes[i].Label = RedactCaptureText(progress.Changes[i].Label)
			progress.Changes[i].PrevLabel = RedactCaptureText(progress.Changes[i].PrevLabel)
		}
		msg.ProgressUpdate = &progress
	}
	if msg.IndexWarming != nil {
		warming := *msg.IndexWarming
		warming.Topic = RedactCaptureText(warming.Topic)
		warming.Hosts = redactCaptureStrings(warming.Hosts)
		warming.SkipReason = RedactCaptureText(warming.SkipReason)
		msg.IndexWarming = &warming
	}
	if msg.Blueprint != nil {
		plan := *msg.Blueprint
		plan.BlueprintPath = RedactCaptureText(plan.BlueprintPath)
		plan.PhaseLabel = RedactCaptureText(plan.PhaseLabel)
		plan.BlueprintTitle = RedactCaptureText(plan.BlueprintTitle)
		msg.Blueprint = &plan
	}
	if msg.CompletionReport != nil {
		report := *msg.CompletionReport
		report.Phase = RedactCaptureText(report.Phase)
		msg.CompletionReport = &report
	}

	msg.ToolResult = redactCaptureToolResult(msg.ToolResult)

	if len(msg.ToolCalls) > 0 {
		calls := make([]api.ToolCall, len(msg.ToolCalls))
		copy(calls, msg.ToolCalls)
		for i := range calls {
			calls[i].Args = redactToolArgs(calls[i].Args)
		}
		msg.ToolCalls = calls
	}

	if msg.WorkflowFeedback != nil {
		feedback := *msg.WorkflowFeedback
		feedback.Prompt = RedactCaptureText(feedback.Prompt)
		feedback.Answer = RedactCaptureText(feedback.Answer)
		feedback.Purpose = RedactCaptureText(feedback.Purpose)
		feedback.Options = append([]string(nil), feedback.Options...)
		for i := range feedback.Options {
			feedback.Options[i] = RedactCaptureText(feedback.Options[i])
		}
		if feedback.Secret != nil {
			secret := *feedback.Secret
			secret.Name = RedactCaptureText(secret.Name)
			secret.Purpose = RedactCaptureText(secret.Purpose)
			feedback.Secret = &secret
		}
		msg.WorkflowFeedback = &feedback
	}
	if msg.WorkerSummary != nil {
		summary := *msg.WorkerSummary
		summary.Envelope = RedactCaptureText(summary.Envelope)
		summary.Grounding = redactCaptureGrounding(summary.Grounding)
		summary.SourceContext = redactSourceContext(summary.SourceContext)
		msg.WorkerSummary = &summary
	}
	msg.Grounding = redactCaptureGrounding(msg.Grounding)
	msg.SourceContext = redactSourceContext(msg.SourceContext)
	if len(msg.NavigationRefs) > 0 {
		msg.NavigationRefs = append([]api.NavigationReference(nil), msg.NavigationRefs...)
		for i := range msg.NavigationRefs {
			msg.NavigationRefs[i].Mention = RedactCaptureText(msg.NavigationRefs[i].Mention)
			msg.NavigationRefs[i].Path = RedactCaptureText(msg.NavigationRefs[i].Path)
			msg.NavigationRefs[i].Candidates = append([]api.NavigationTarget(nil), msg.NavigationRefs[i].Candidates...)
			for j := range msg.NavigationRefs[i].Candidates {
				msg.NavigationRefs[i].Candidates[j].Path = RedactCaptureText(msg.NavigationRefs[i].Candidates[j].Path)
			}
		}
	}

	return msg
}

// redactToolArgs preserves argument shape while scrubbing values.
func redactToolArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return args
	}
	scrubbed, ok := RedactCaptureValue(args).(map[string]any)
	if !ok {
		return args
	}
	return scrubbed
}

func redactSourceContext(source *api.SourceContext) *api.SourceContext {
	if source == nil {
		return nil
	}
	out := *source
	out.Locations = append([]api.NavigationTarget(nil), source.Locations...)
	for i := range out.Locations {
		out.Locations[i].Path = RedactCaptureText(out.Locations[i].Path)
	}
	return &out
}
