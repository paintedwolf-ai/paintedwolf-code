package llm

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

// Field paths locate redactions in message wire data.
const (
	fieldContent          = "content"
	fieldContentParts     = "content_parts"
	fieldToolResult       = "tool_result"
	fieldToolResultArgs   = "tool_result.tool_args"
	fieldToolResultVisual = "tool_result.visual"
	fieldToolCalls        = "tool_calls"
	fieldToolCallArgs     = "args"
	fieldToolCallExtra    = "extra_content"
	fieldWorkflowFeedback = "workflow_feedback"
	fieldCheckpoint       = "tool_result.checkpoint_decision"
	fieldReasoningDropped = "model_reasoning"
)

func indexedField(parts ...string) string { return strings.Join(parts, ".") }

func atIndex(base string, i int) string { return base + "." + strconv.Itoa(i) }

// fieldOrigin records provenance for a visited field.
type fieldOrigin struct {
	kind secretmatch.SourceKind
	tool string
}

// fieldScreen applies one decision to each visited field.
type fieldScreen interface {
	// text screens a field that is rewritten in place.
	text(origin fieldOrigin, field, label, value string) string
	// whole screens a field that can only be kept or dropped intact.
	whole(origin fieldOrigin, field string, values []string) bool
}

// requestPass is a field screen plus the per-message bookkeeping one pass needs.
type requestPass interface {
	fieldScreen
	// enter receives the source message before its fields are walked.
	enter(msg api.Message)
	// leave receives the source message and the copy the walk produced.
	leave(source api.Message, screened *api.Message)
}

// messageRedactor walks every screened field of a message or tool definition.
type messageRedactor struct {
	screen fieldScreen
	origin fieldOrigin
}

// text screens one field and returns the copy that replaces it.
func (r *messageRedactor) text(field, label, value string) string {
	return r.screen.text(r.origin, field, label, value)
}

// within walks a subtree whose fields answer to a different origin.
func (r *messageRedactor) within(origin fieldOrigin, walk func()) {
	prev := r.origin
	r.origin = origin
	walk()
	r.origin = prev
}

// redactionScreen rewrites each field and records a span per replacement.
type redactionScreen struct {
	ctx     context.Context
	matcher *secretmatch.Matcher
	keep    func(secretmatch.Match) bool
	written []writtenSpan
	// definitions records markers in tool definitions, which carry no message
	// metadata of their own.
	definitions secretReplacements
	// mark is the written count when the current message's walk began.
	mark int
}

// writtenSpan is one recorded replacement and the input range it covered.
type writtenSpan struct {
	span                   api.RedactedSpan
	sourceStart, sourceEnd int
	// wholeField means the field was dropped, not rewritten.
	wholeField bool
}

// keepOnlyReferences leaves every match except live managed values for the
// approval screen.
func keepOnlyReferences(secretmatch.Match) bool { return false }

func (s *redactionScreen) text(origin fieldOrigin, field, label, value string) string {
	out, replacements := s.matcher.ProjectLabeledWhere(s.ctx, label, value, s.keep)
	for _, replacement := range replacements {
		span := api.RedactedSpan{
			Field:     field,
			Start:     replacement.Start,
			Length:    replacement.Length,
			Kind:      api.RedactionKindSecret,
			Source:    api.RedactionSource(replacement.Source),
			RuleID:    replacement.RuleID,
			RuleTitle: replacement.Title,
		}
		if replacement.Reference != "" {
			span.Kind = api.RedactionKindManagedReference
		}
		if origin.kind == secretmatch.SourceToolDefinition {
			s.definitions.add(span.Kind)
		}
		s.written = append(s.written, writtenSpan{
			span: span, sourceStart: replacement.SourceStart, sourceEnd: replacement.SourceEnd,
		})
	}
	return out
}

func (s *redactionScreen) whole(_ fieldOrigin, field string, values []string) bool {
	matches := 0
	for _, value := range values {
		_, replacements := s.matcher.ProjectLabeledWhere(s.ctx, "", value, s.keep)
		matches += len(replacements)
	}
	if matches == 0 {
		return false
	}
	s.written = append(s.written, writtenSpan{
		span: api.RedactedSpan{
			Field:  field,
			Start:  0,
			Length: secretmatch.PlaceholderRunes(),
			Kind:   api.RedactionKindSecret,
			Source: api.RedactionSourceShapeRule,
		},
		wholeField: true,
	})
	return true
}

func (s *redactionScreen) enter(api.Message) { s.mark = len(s.written) }

// leave stamps a message with the markers its own fields produced.
func (s *redactionScreen) leave(source api.Message, screened *api.Message) {
	screened.HostSecretRedaction = api.NewHostSecretRedactionMeta(
		reconcileSpans(s.written[s.mark:], source.HostSecretRedaction))
}

// stringMap redacts strings with stable key-based paths.
func (r *messageRedactor) stringMap(field string, values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(values))
	for i, key := range keys {
		safeKey := r.text(indexedField(atIndex(field, i), "key"), "", key)
		safeKey = uniqueRedactedMapKey(out, safeKey)
		out[safeKey] = r.anyValue(indexedField(field, safeKey), key, values[key])
	}
	return out
}

func uniqueRedactedMapKey(values map[string]any, key string) string {
	if _, exists := values[key]; !exists {
		return key
	}
	for i := 2; ; i++ {
		candidate := key + "#" + strconv.Itoa(i)
		if _, exists := values[candidate]; !exists {
			return candidate
		}
	}
}

func (r *messageRedactor) anyValue(field, label string, value any) any {
	switch typed := value.(type) {
	case string:
		return r.text(field, label, typed)
	case []string:
		out := make([]string, len(typed))
		for i := range typed {
			out[i] = r.text(atIndex(field, i), label, typed[i])
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = r.anyValue(atIndex(field, i), label, typed[i])
		}
		return out
	case map[string]any:
		return r.stringMap(field, typed)
	default:
		return value
	}
}

func (r *messageRedactor) strings(field string, values []string) []string {
	if values == nil {
		return nil
	}
	out := append([]string(nil), values...)
	for i := range out {
		out[i] = r.text(atIndex(field, i), "", out[i])
	}
	return out
}

func (r *messageRedactor) grounding(field string, source *api.CitationGrounding) *api.CitationGrounding {
	if source == nil {
		return nil
	}
	out := *source
	out.ObservedPathsSample = r.strings(indexedField(field, "observed_paths_sample"), source.ObservedPathsSample)
	out.ObservedURLsSample = r.strings(indexedField(field, "observed_urls_sample"), source.ObservedURLsSample)
	out.CitedURLs = r.strings(indexedField(field, "cited_urls"), source.CitedURLs)
	out.ProseLeaksSample = r.strings(indexedField(field, "prose_leaks_sample"), source.ProseLeaksSample)
	out.ProseAdvisoriesSample = r.strings(indexedField(field, "prose_advisories_sample"), source.ProseAdvisoriesSample)
	out.Checks = append([]api.CitationGroundingCheck(nil), source.Checks...)
	for i := range out.Checks {
		base := atIndex(indexedField(field, "checks"), i)
		out.Checks[i].Label = r.text(indexedField(base, "label"), "", out.Checks[i].Label)
		out.Checks[i].Summary = r.text(indexedField(base, "summary"), "", out.Checks[i].Summary)
		out.Checks[i].Matched = r.strings(indexedField(base, "matched"), out.Checks[i].Matched)
		out.Checks[i].Failed = r.strings(indexedField(base, "failed"), out.Checks[i].Failed)
	}
	out.Findings = append([]api.CitationGroundingFinding(nil), source.Findings...)
	for i := range out.Findings {
		base := atIndex(indexedField(field, "findings"), i)
		out.Findings[i].Path = r.text(indexedField(base, "path"), "", out.Findings[i].Path)
		out.Findings[i].Excerpt = r.text(indexedField(base, "excerpt"), "", out.Findings[i].Excerpt)
		out.Findings[i].Note = r.text(indexedField(base, "note"), "", out.Findings[i].Note)
	}
	out.CitedEvidence = append([]api.CitationGroundingCitedEvidence(nil), source.CitedEvidence...)
	for i := range out.CitedEvidence {
		base := atIndex(indexedField(field, "cited_evidence"), i)
		out.CitedEvidence[i].Path = r.text(indexedField(base, "path"), "", out.CitedEvidence[i].Path)
		out.CitedEvidence[i].Excerpt = r.text(indexedField(base, "excerpt"), "", out.CitedEvidence[i].Excerpt)
	}
	out.EvidenceRecords = append([]api.CitationGroundingEvidenceRecord(nil), source.EvidenceRecords...)
	for i := range out.EvidenceRecords {
		base := atIndex(indexedField(field, "evidence_records"), i)
		out.EvidenceRecords[i].Path = r.text(indexedField(base, "path"), "", out.EvidenceRecords[i].Path)
		out.EvidenceRecords[i].URLs = r.strings(indexedField(base, "urls"), out.EvidenceRecords[i].URLs)
		out.EvidenceRecords[i].Excerpt = r.text(indexedField(base, "excerpt"), "", out.EvidenceRecords[i].Excerpt)
	}
	return &out
}

func (r *messageRedactor) toolResult(source *api.ToolResult) *api.ToolResult {
	if source == nil {
		return nil
	}
	out := *source
	out.Content = r.text(indexedField(fieldToolResult, fieldContent), "", source.Content)
	out.ToolArgs = r.stringMap(fieldToolResultArgs, source.ToolArgs)
	out.DisplaySubject = r.text("tool_result.display_subject", "", source.DisplaySubject)
	out.Feedback = append([]api.ToolFeedback(nil), source.Feedback...)
	for i := range out.Feedback {
		base := atIndex(indexedField(fieldToolResult, "feedback"), i)
		out.Feedback[i].Details = r.stringMap(indexedField(base, "details"), out.Feedback[i].Details)
		if out.Feedback[i].Subject != nil {
			subject := *out.Feedback[i].Subject
			subject.ID = r.text(indexedField(base, "subject.id"), "", subject.ID)
			out.Feedback[i].Subject = &subject
		}
	}
	if source.Invocation != nil {
		invocation := *source.Invocation
		invocation.Evidence.Ref = r.text("tool_result.invocation.evidence.ref", "", invocation.Evidence.Ref)
		invocation.Evidence.OwnerRef = r.text("tool_result.invocation.evidence.owner_ref", "", invocation.Evidence.OwnerRef)
		if invocation.Failure != nil {
			failure := *invocation.Failure
			failure.OwnerRef = r.text("tool_result.invocation.failure.owner_ref", "", failure.OwnerRef)
			failure.Details = r.stringMap("tool_result.invocation.failure.details", failure.Details)
			invocation.Failure = &failure
		}
		out.Invocation = &invocation
	}
	if source.FileEdit != nil {
		edit := r.fileEdit(*source.FileEdit, "tool_result.file_edit")
		out.FileEdit = &edit
	}
	if source.OverlayPromotion != nil {
		promotion := *source.OverlayPromotion
		promotion.Files = make([]api.FileEditSnapshot, len(source.OverlayPromotion.Files))
		for i, file := range source.OverlayPromotion.Files {
			promotion.Files[i] = r.fileEdit(file, atIndex("tool_result.overlay_promotion.files", i))
		}
		out.OverlayPromotion = &promotion
	}
	if source.Visual != nil {
		visual := *source.Visual
		visual.Caption = r.text(indexedField(fieldToolResultVisual, "caption"), "", visual.Caption)
		out.Visual = &visual
	}
	if source.CheckpointDecision != nil {
		decision := *source.CheckpointDecision
		decision.Tool = r.text(indexedField(fieldCheckpoint, "tool"), "", decision.Tool)
		decision.Subject = r.text(indexedField(fieldCheckpoint, "subject"), "", decision.Subject)
		decision.CausingCommand = r.text(indexedField(fieldCheckpoint, "causing_command"), "", decision.CausingCommand)
		decision.Location = r.text(indexedField(fieldCheckpoint, "location"), "", decision.Location)
		decision.Guidance = r.text(indexedField(fieldCheckpoint, "guidance"), "", decision.Guidance)
		decision.GrantTitle = r.text(indexedField(fieldCheckpoint, "grant_title"), "", decision.GrantTitle)
		out.CheckpointDecision = &decision
	}
	out.ExternalAccess = r.externalAccess(source.ExternalAccess)
	if source.Skill != nil {
		skill := *source.Skill
		skill.Description = r.text("tool_result.skill.description", "", skill.Description)
		skill.Instructions = r.text("tool_result.skill.instructions", "", skill.Instructions)
		skill.Dir = r.text("tool_result.skill.dir", "", skill.Dir)
		skill.Resources = r.strings("tool_result.skill.resources", skill.Resources)
		skill.License = r.text("tool_result.skill.license", "", skill.License)
		skill.Compatibility = r.text("tool_result.skill.compatibility", "", skill.Compatibility)
		skill.AllowedTools = r.text("tool_result.skill.allowed_tools", "", skill.AllowedTools)
		out.Skill = &skill
	}
	if source.Verdict != nil {
		verdict := *source.Verdict
		verdict.EvidenceKey = r.text("tool_result.verdict.evidence_key", "", verdict.EvidenceKey)
		verdict.Phase = r.text("tool_result.verdict.phase", "", verdict.Phase)
		verdict.Grounding = r.grounding("tool_result.verdict.grounding", verdict.Grounding)
		out.Verdict = &verdict
	}
	return &out
}

func (r *messageRedactor) externalAccess(source *api.ExternalAccess) *api.ExternalAccess {
	if source == nil {
		return nil
	}
	out := *source
	out.DeclaredDestinations = r.strings("tool_result.external_access.declared_destinations", source.DeclaredDestinations)
	out.Endpoints = append([]api.ExternalAccessEndpoint(nil), source.Endpoints...)
	for i := range out.Endpoints {
		out.Endpoints[i].Host = r.text(atIndex("tool_result.external_access.endpoints", i)+".host", "", out.Endpoints[i].Host)
	}
	out.Sockets = append([]api.ExternalAccessSocket(nil), source.Sockets...)
	for i := range out.Sockets {
		base := atIndex("tool_result.external_access.sockets", i)
		out.Sockets[i].ApprovedPath = r.text(base+".approved_path", "", out.Sockets[i].ApprovedPath)
		out.Sockets[i].ResolvedPath = r.text(base+".resolved_path", "", out.Sockets[i].ResolvedPath)
	}
	out.Detections = append([]api.ExternalAccessDetection(nil), source.Detections...)
	for i := range out.Detections {
		out.Detections[i].RuleTitle = r.text(atIndex("tool_result.external_access.detections", i)+".rule_title", "", out.Detections[i].RuleTitle)
	}
	return &out
}

// redactMessageFields returns a screened copy and how many replacements it wrote.
func redactMessageFields(
	ctx context.Context,
	matcher *secretmatch.Matcher,
	msg api.Message,
	keep func(secretmatch.Match) bool,
) (api.Message, int) {
	screen := &redactionScreen{ctx: ctx, matcher: matcher, keep: keep}
	screen.enter(msg)
	out := (&messageRedactor{screen: screen}).message(msg)
	screen.leave(msg, &out)
	return out, len(screen.written)
}

// message walks every screened field of one message and returns the copy.
func (r *messageRedactor) message(msg api.Message) api.Message {
	out := msg
	out.Content = r.text(fieldContent, "", msg.Content)
	if len(msg.ContentParts) > 0 {
		out.ContentParts = append([]api.MessageContentPart(nil), msg.ContentParts...)
		for i := range out.ContentParts {
			base := atIndex(fieldContentParts, i)
			out.ContentParts[i].Content = r.text(indexedField(base, fieldContent), "", out.ContentParts[i].Content)
			out.ContentParts[i].Path = r.text(indexedField(base, "path"), "", out.ContentParts[i].Path)
			out.ContentParts[i].SourceRef = r.text(indexedField(base, "source_ref"), "", out.ContentParts[i].SourceRef)
		}
	}
	if msg.WorkflowBoundary != nil {
		boundary := *msg.WorkflowBoundary
		boundary.Phase = r.text("workflow_boundary.phase", "", boundary.Phase)
		boundary.Reason = r.text("workflow_boundary.reason", "", boundary.Reason)
		out.WorkflowBoundary = &boundary
	}
	if msg.ProgressComplete != nil {
		progress := *msg.ProgressComplete
		progress.Steps = append([]api.ProgressStep(nil), msg.ProgressComplete.Steps...)
		for i := range progress.Steps {
			progress.Steps[i].Label = r.text(atIndex("progress_complete.steps", i)+".label", "", progress.Steps[i].Label)
		}
		out.ProgressComplete = &progress
	}
	if msg.ProgressUpdate != nil {
		progress := *msg.ProgressUpdate
		progress.Steps = append([]api.ProgressStep(nil), msg.ProgressUpdate.Steps...)
		for i := range progress.Steps {
			progress.Steps[i].Label = r.text(atIndex("progress_update.steps", i)+".label", "", progress.Steps[i].Label)
		}
		progress.Changes = append([]api.ProgressChange(nil), msg.ProgressUpdate.Changes...)
		for i := range progress.Changes {
			base := atIndex("progress_update.changes", i)
			progress.Changes[i].Label = r.text(base+".label", "", progress.Changes[i].Label)
			progress.Changes[i].PrevLabel = r.text(base+".prev_label", "", progress.Changes[i].PrevLabel)
		}
		out.ProgressUpdate = &progress
	}
	if msg.IndexWarming != nil {
		warming := *msg.IndexWarming
		warming.Topic = r.text("index_warming.topic", "", warming.Topic)
		warming.Hosts = r.strings("index_warming.hosts", warming.Hosts)
		warming.SkipReason = r.text("index_warming.skip_reason", "", warming.SkipReason)
		out.IndexWarming = &warming
	}
	if msg.Blueprint != nil {
		plan := *msg.Blueprint
		plan.BlueprintPath = r.text("blueprint.blueprint_path", "", plan.BlueprintPath)
		plan.PhaseLabel = r.text("blueprint.phase_label", "", plan.PhaseLabel)
		plan.BlueprintTitle = r.text("blueprint.blueprint_title", "", plan.BlueprintTitle)
		out.Blueprint = &plan
	}
	if msg.CompletionReport != nil {
		report := *msg.CompletionReport
		report.Phase = r.text("completion_report.phase", "", report.Phase)
		out.CompletionReport = &report
	}
	out.ToolResult = r.toolResult(msg.ToolResult)
	if len(msg.ToolCalls) > 0 {
		out.ToolCalls = append([]api.ToolCall(nil), msg.ToolCalls...)
		for i := range out.ToolCalls {
			base := atIndex(fieldToolCalls, i)
			r.within(fieldOrigin{kind: secretmatch.SourceToolCall, tool: msg.ToolCalls[i].Name}, func() {
				out.ToolCalls[i].Args = r.stringMap(indexedField(base, fieldToolCallArgs), msg.ToolCalls[i].Args)
				out.ToolCalls[i].ExtraContent = r.stringMap(indexedField(base, fieldToolCallExtra), msg.ToolCalls[i].ExtraContent)
			})
		}
	}
	if msg.WorkflowFeedback != nil {
		feedback := *msg.WorkflowFeedback
		feedback.Prompt = r.text(indexedField(fieldWorkflowFeedback, "prompt"), "", feedback.Prompt)
		feedback.Answer = r.text(indexedField(fieldWorkflowFeedback, "answer"), "", feedback.Answer)
		feedback.Purpose = r.text(indexedField(fieldWorkflowFeedback, "purpose"), "", feedback.Purpose)
		feedback.Options = append([]string(nil), feedback.Options...)
		for i := range feedback.Options {
			feedback.Options[i] = r.text(atIndex(indexedField(fieldWorkflowFeedback, "options"), i), "", feedback.Options[i])
		}
		if feedback.Secret != nil {
			secret := *feedback.Secret
			secret.Name = r.text(indexedField(fieldWorkflowFeedback, "secret.name"), "", secret.Name)
			secret.Purpose = r.text(indexedField(fieldWorkflowFeedback, "secret.purpose"), "", secret.Purpose)
			feedback.Secret = &secret
		}
		out.WorkflowFeedback = &feedback
	}
	if msg.WorkerSummary != nil {
		summary := *msg.WorkerSummary
		summary.Envelope = r.text("worker_summary.envelope", "", summary.Envelope)
		summary.SourceContext = r.sourceContext("worker_summary.source_context", summary.SourceContext)
		summary.Grounding = r.grounding("worker_summary.grounding", summary.Grounding)
		out.WorkerSummary = &summary
	}
	out.Grounding = r.grounding("grounding", msg.Grounding)
	out.SourceContext = r.sourceContext("source_context", msg.SourceContext)
	if len(msg.NavigationRefs) > 0 {
		out.NavigationRefs = append([]api.NavigationReference(nil), msg.NavigationRefs...)
		for i := range out.NavigationRefs {
			base := atIndex("navigation_refs", i)
			out.NavigationRefs[i].Mention = r.text(base+".mention", "", out.NavigationRefs[i].Mention)
			out.NavigationRefs[i].Path = r.text(base+".path", "", out.NavigationRefs[i].Path)
			out.NavigationRefs[i].Candidates = append([]api.NavigationTarget(nil), out.NavigationRefs[i].Candidates...)
			for j := range out.NavigationRefs[i].Candidates {
				target := &out.NavigationRefs[i].Candidates[j]
				target.Path = r.text(atIndex(base+".candidates", j)+".path", "", target.Path)
			}
		}
	}
	out.ModelReasoning = r.modelReasoning(msg.ModelReasoning)
	return out
}

// modelReasoning drops a trace if any field matches.
func (r *messageRedactor) modelReasoning(source *api.ModelReasoning) *api.ModelReasoning {
	if source == nil {
		return nil
	}
	values := make([]string, 0, 1+len(source.Details))
	values = append(values, source.Text)
	for _, detail := range source.Details {
		values = append(values, string(detail))
	}
	if r.screen.whole(r.origin, fieldReasoningDropped, values) {
		return nil
	}
	return source
}

// reconcileSpans carries earlier markers into this copy. Each moves by the width
// change of fresh replacements before it in its field; one a fresh replacement
// covers, or in a dropped field, is superseded.
func reconcileSpans(fresh []writtenSpan, prior *api.HostSecretRedactionMeta) []api.RedactedSpan {
	spans := make([]api.RedactedSpan, 0, len(fresh)+prior.Occurrences())
	for _, written := range fresh {
		spans = append(spans, written.span)
	}
	for _, span := range prior.SpanList() {
		if moved, kept := carrySpan(span, fresh); kept {
			spans = append(spans, moved)
		}
	}
	return spans
}

func carrySpan(span api.RedactedSpan, fresh []writtenSpan) (api.RedactedSpan, bool) {
	shift := 0
	for _, written := range fresh {
		if written.span.Field != span.Field {
			continue
		}
		if written.wholeField {
			return span, false
		}
		if written.sourceStart < span.Start+span.Length && span.Start < written.sourceEnd {
			return span, false
		}
		if written.sourceEnd <= span.Start {
			shift += written.span.Length - (written.sourceEnd - written.sourceStart)
		}
	}
	span.Start += shift
	return span, true
}

func (r *messageRedactor) fileEdit(edit api.FileEditSnapshot, field string) api.FileEditSnapshot {
	edit.Path = r.text(indexedField(field, "path"), "", edit.Path)
	edit.After = r.text(indexedField(field, "after"), "", edit.After)
	if edit.Before != nil {
		before := r.text(indexedField(field, "before"), "", *edit.Before)
		edit.Before = &before
	}
	return edit
}

func (r *messageRedactor) sourceContext(field string, source *api.SourceContext) *api.SourceContext {
	if source == nil {
		return nil
	}
	out := *source
	out.Locations = []api.NavigationTarget{}
	for i, target := range source.Locations {
		screened := r.text(atIndex(indexedField(field, "locations"), i)+".path", "", target.Path)
		if screened != target.Path {
			out.Truncated = true
			continue
		}
		out.Locations = append(out.Locations, target)
	}
	return &out
}
