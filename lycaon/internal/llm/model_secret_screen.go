package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrModelRequestSecretUnanswered means the approval remained unanswered.
var ErrModelRequestSecretUnanswered = errors.New("model request not sent: credential card went unanswered")

// ErrModelRequestSecretWithheld means the user declined the send.
var ErrModelRequestSecretWithheld = errors.New("model request not sent: user declined to send a detected credential")

// ErrModelRequestSecretScreenFailed means the approval could not be presented.
var ErrModelRequestSecretScreenFailed = errors.New("model request not sent: credential screen could not ask")

// ModelRequestSecretScreenFaultError names the host step that failed.
type ModelRequestSecretScreenFaultError struct {
	Stage string
	Err   error
}

func (e *ModelRequestSecretScreenFaultError) Error() string {
	msg := ErrModelRequestSecretScreenFailed.Error()
	if strings.TrimSpace(e.Stage) != "" {
		msg += " (" + e.Stage + ")"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *ModelRequestSecretScreenFaultError) Unwrap() []error {
	return []error{ErrModelRequestSecretScreenFailed, e.Err}
}

// ModelRequestSecretWithheldError carries guidance attached to a refusal.
type ModelRequestSecretWithheldError struct {
	Guidance string
}

func (e *ModelRequestSecretWithheldError) Error() string {
	if strings.TrimSpace(e.Guidance) == "" {
		return ErrModelRequestSecretWithheld.Error()
	}
	return ErrModelRequestSecretWithheld.Error() + ": " + e.Guidance
}

func (e *ModelRequestSecretWithheldError) Unwrap() error { return ErrModelRequestSecretWithheld }

// SecretScreenBlocked identifies refusals and approval faults that prohibit destination failover.
func SecretScreenBlocked(err error) bool {
	return errors.Is(err, ErrModelRequestSecretWithheld) ||
		errors.Is(err, ErrModelRequestSecretUnanswered) ||
		errors.Is(err, ErrModelRequestSecretScreenFailed)
}

// SecretWithheldGuidance returns guidance from a withheld request.
func SecretWithheldGuidance(err error) string {
	var withheld *ModelRequestSecretWithheldError
	if errors.As(err, &withheld) {
		return strings.TrimSpace(withheld.Guidance)
	}
	return ""
}

// ScreenDestination binds screening to the resolved provider.
type ScreenDestination struct {
	ID    string
	Label string
	// ProviderID is the configured instance the request resolves to; the
	// secret card offers device-wide trust for it by this id.
	ProviderID string
	Trusted    bool
}

// OutboundSecretScreen transforms or rejects one provider-bound request.
type OutboundSecretScreen interface {
	Screen(ctx context.Context, destination ScreenDestination, req modelcall.CompletionRequest) (modelcall.CompletionRequest, error)
}

// ManagedSecretEvidence refreshes screening before a provider request.
type ManagedSecretEvidence func(ctx context.Context, projectID, rootSessionID string) ([]secretmatch.Remembered, error)

// ManagedSecretAdoptRequest carries a detected value only across the protected
// adoption callback. Implementations return a value-free reference.
type ManagedSecretAdoptRequest struct {
	ProjectID, RootSessionID, SessionID, OperationID string
	Name, Purpose, Value                             string
}

type ManagedSecretAdopter func(ctx context.Context, req ManagedSecretAdoptRequest) (reference string, err error)

// ModelSecretScreen applies the matcher immediately before a request leaves.
type ModelSecretScreen struct {
	matcher  *secretmatch.Matcher
	ask      secretmatch.AskFunc
	evidence ManagedSecretEvidence
	adopter  ManagedSecretAdopter
}

type secretScreenedProvider struct {
	inner       modelcall.Provider
	destination ScreenDestination
	screen      OutboundSecretScreen
}

func (p *secretScreenedProvider) ID() string                       { return p.inner.ID() }
func (p *secretScreenedProvider) Models() []modelcall.ModelInfo    { return p.inner.Models() }
func (p *secretScreenedProvider) Profile() providerprofile.Profile { return p.inner.Profile() }

func (p *secretScreenedProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	started := time.Now()
	screened, err := p.screen.Screen(ctx, p.destination, req)
	lifecycleFromContext(ctx).record(ctx, "screened", "screen_ms", time.Since(started).Milliseconds(), "failed", err != nil)
	if err != nil {
		return nil, err
	}
	return p.inner.Complete(ctx, screened)
}

func (p *secretScreenedProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	started := time.Now()
	screened, err := p.screen.Screen(ctx, p.destination, req)
	lifecycleFromContext(ctx).record(ctx, "screened", "screen_ms", time.Since(started).Milliseconds(), "failed", err != nil)
	if err != nil {
		return nil, err
	}
	return p.inner.Stream(ctx, screened)
}

func NewModelSecretScreen(matcher *secretmatch.Matcher, ask secretmatch.AskFunc) *ModelSecretScreen {
	return &ModelSecretScreen{matcher: matcher, ask: ask}
}

// SetManagedSecretEvidence installs the provider-bound refresh.
func (s *ModelSecretScreen) SetManagedSecretEvidence(evidence ManagedSecretEvidence) {
	if s != nil {
		s.evidence = evidence
	}
}

// SetManagedSecretAdopter installs protected storage for detected values.
func (s *ModelSecretScreen) SetManagedSecretAdopter(adopter ManagedSecretAdopter) {
	if s != nil {
		s.adopter = adopter
	}
}

type locatedModelSecret struct {
	match secretmatch.Match
	// value exists only during this screening pass.
	value            string
	label            string
	sourceKind       secretmatch.SourceKind
	sourceTool       string
	sourcePath       string
	sourceLine       int
	sourceToolCallID string
	source           gate.SecretSource
}

// Screen asks at most once per request and modifies only the provider-bound
// copy. Destination trust and host composition reach the gate as alert facts.
func (s *ModelSecretScreen) Screen(ctx context.Context, destination ScreenDestination, req modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
	if s == nil {
		return req, nil
	}
	attr := curationctx.SessionFrom(ctx)
	sessionID := firstNonEmpty(req.Debug.SessionID, attr.SessionID)
	projectID := firstNonEmpty(req.Debug.ProjectID, attr.ProjectID)
	projectDir := firstNonEmpty(req.Debug.ProjectDir, attr.ProjectDir)
	rootSessionID := firstNonEmpty(req.Debug.RootSessionID, req.Debug.ParentSessionID, attr.ParentSessionID, sessionID)
	ctx = secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{
		SessionID: sessionID, RootSessionID: rootSessionID,
		ProjectID: projectID, ProjectDir: projectDir,
	})
	if s.evidence != nil && projectID != "" {
		values, err := s.evidence(ctx, projectID, rootSessionID)
		if err != nil {
			return modelcall.CompletionRequest{}, &ModelRequestSecretScreenFaultError{
				Stage: secretmatch.FaultStageManagedSecretStore,
				Err:   err,
			}
		}
		ctx = secretmatch.WithScreeningValues(ctx, values)
	}
	if s.matcher.Inert() {
		return req, nil
	}
	classification := s.matcher.ClassificationSnapshot(ctx)
	ctx = s.matcher.WithClassifications(ctx, classification)
	req = redactModelRequest(ctx, s.matcher, req, keepOnlyReferences)
	hits := inspectModelRequest(ctx, s.matcher, req)
	if len(hits) == 0 {
		return req, nil
	}
	if containsNonDisclosable(hits) {
		req = redactModelRequest(ctx, s.matcher, req, func(hit secretmatch.Match) bool {
			return hit.NonDisclosable
		})
		hits = inspectModelRequest(ctx, s.matcher, req)
		if len(hits) == 0 {
			return req, nil
		}
	}
	primary := primaryModelSecret(hits)
	// Evidence persists even when the approval remains unanswered.
	rememberModelSecrets(s.matcher, rootSessionID, hits)
	destinationID := strings.TrimSpace(destination.ID)
	destinationLabel := firstNonEmpty(destination.Label, destinationID)
	safeDestinationID := s.matcher.RedactString(ctx, destinationID)
	safeDestinationLabel := s.matcher.RedactString(ctx, destinationLabel)
	if s.ask == nil {
		return modelcall.CompletionRequest{}, &ModelRequestSecretScreenFaultError{
			Stage: secretmatch.FaultStageScreenUnwired,
		}
	}
	resolution, err := s.ask(ctx, secretmatch.Alert{
		ReviewValue:        primary.value,
		SessionID:          sessionID,
		RootSessionID:      rootSessionID,
		ProjectID:          projectID,
		ProjectDir:         projectDir,
		Surface:            secretmatch.SurfaceModel,
		DestinationID:      safeDestinationID,
		DestinationLabel:   safeDestinationLabel,
		ProviderID:         destination.ProviderID,
		DestinationTrusted: destination.Trusted,
		HostComposed:       req.Composition == modelcall.CompositionHostUtility,
		RuleID:             primary.match.RuleID,
		RuleTitle:          primary.match.Title,
		GenericShape:       primary.match.GenericShape,
		VarName:            primary.match.VarName,
		Container:          primary.match.Container,
		Occurrences:        len(hits),
		SourceKind:         primary.sourceKind,
		SourceTool:         s.matcher.RedactString(ctx, primary.sourceTool),
		SourcePath:         s.matcher.RedactString(ctx, primary.sourcePath),
		SourceLine:         primary.sourceLine,
		OriginKind:         modelOriginKind(primary.sourcePath),
		SourceToolCallID:   primary.sourceToolCallID,
		Source:             primary.source,
		Fingerprints:       modelSecretFingerprints(hits),
	})
	if err != nil {
		fault := &ModelRequestSecretScreenFaultError{Err: err}
		if f, ok := secretmatch.Faulted(err); ok {
			fault.Stage, fault.Err = f.Stage, f.Err
		}
		return modelcall.CompletionRequest{}, fault
	}
	if err := s.matcher.CheckClassification(ctx, classification); err != nil {
		return modelcall.CompletionRequest{}, &ModelRequestSecretScreenFaultError{Stage: secretmatch.FaultStageRaise, Err: err}
	}
	switch resolution.Decision {
	case secretmatch.SendUnchanged:
		return req, nil
	case secretmatch.SendRedacted:
		return redactModelRequest(ctx, s.matcher, req, nil), nil
	case secretmatch.TrackAndReplace:
		return s.trackAndReplace(ctx, req, projectID, rootSessionID, sessionID, hits)
	case secretmatch.Withhold:
		// A refusal sends neither the original nor a redacted copy.
		return modelcall.CompletionRequest{}, &ModelRequestSecretWithheldError{Guidance: resolution.Guidance}
	case secretmatch.Unanswered:
		return modelcall.CompletionRequest{}, ErrModelRequestSecretUnanswered
	default:
		return modelcall.CompletionRequest{}, &ModelRequestSecretScreenFaultError{
			Stage: secretmatch.FaultStageRaise,
			Err:   fmt.Errorf("unknown secret screen decision %q", resolution.Decision),
		}
	}
}

func (s *ModelSecretScreen) trackAndReplace(
	ctx context.Context,
	req modelcall.CompletionRequest,
	projectID, rootSessionID, sessionID string,
	hits []locatedModelSecret,
) (modelcall.CompletionRequest, error) {
	if s.adopter == nil {
		return modelcall.CompletionRequest{}, &ModelRequestSecretScreenFaultError{Stage: secretmatch.FaultStageManagedSecretStore}
	}
	seen := map[string]struct{}{}
	for _, hit := range hits {
		if hit.value == "" {
			continue
		}
		if _, ok := seen[hit.value]; ok {
			continue
		}
		seen[hit.value] = struct{}{}
		if hit.match.Fingerprint == "" {
			return modelcall.CompletionRequest{}, &ModelRequestSecretScreenFaultError{
				Stage: secretmatch.FaultStageManagedSecretStore,
				Err:   fmt.Errorf("detected credential has no protected fingerprint"),
			}
		}
		name := firstNonEmpty(hit.match.VarName, hit.label, hit.match.Title, "Detected credential")
		_, err := s.adopter(ctx, ManagedSecretAdoptRequest{
			ProjectID: projectID, RootSessionID: rootSessionID, SessionID: sessionID,
			OperationID: "detected:" + string(hit.match.Fingerprint),
			Name:        name, Purpose: "Detected in an outbound " + string(hit.sourceKind), Value: hit.value,
		})
		if err != nil {
			return modelcall.CompletionRequest{}, &ModelRequestSecretScreenFaultError{Stage: secretmatch.FaultStageManagedSecretStore, Err: err}
		}
	}
	return redactModelRequest(ctx, s.matcher, req, keepOnlyReferences), nil
}

func containsNonDisclosable(hits []locatedModelSecret) bool {
	for _, hit := range hits {
		if hit.match.NonDisclosable {
			return true
		}
	}
	return false
}

func modelSecretFingerprints(hits []locatedModelSecret) []secretmatch.SecretFingerprint {
	matches := make([]secretmatch.Match, 0, len(hits))
	for _, hit := range hits {
		matches = append(matches, hit.match)
	}
	return secretmatch.Fingerprints(matches)
}

// messageProvenance is what one message's fields say about where they came from.
type messageProvenance struct {
	kind   secretmatch.SourceKind
	tool   string
	path   string
	callID string
	source gate.SecretSource
}

// inspectionScreen records what the walk finds and rewrites nothing.
type inspectionScreen struct {
	ctx     context.Context
	matcher *secretmatch.Matcher
	// message is the provenance of the message currently being walked.
	message messageProvenance
	hits    []locatedModelSecret
}

func (s *inspectionScreen) text(origin fieldOrigin, _, label, value string) string {
	found := s.matcher.ScreenLabeledContext(s.ctx, label, value)
	kind, tool := s.message.kind, s.message.tool
	if origin.kind != "" {
		kind, tool = origin.kind, origin.tool
	}
	for _, hit := range found {
		s.hits = append(s.hits, locatedModelSecret{
			match: hit, value: runeSlice(value, hit.Start, hit.End), label: label,
			sourceKind: kind, sourceTool: tool, sourcePath: s.message.path,
			sourceLine:       physicalLineAtRune(value, hit.Start),
			sourceToolCallID: s.message.callID, source: s.message.source,
		})
	}
	return value
}

func (s *inspectionScreen) whole(origin fieldOrigin, field string, values []string) bool {
	for _, value := range values {
		s.text(origin, field, "", value)
	}
	// An inspecting pass keeps no copy, so it drops nothing.
	return false
}

func (s *inspectionScreen) enter(msg api.Message) {
	tool, path, callID, source := sourceForMessage(msg)
	s.message = messageProvenance{
		kind: sourceKindForMessage(msg), tool: tool,
		path: path, callID: callID, source: source,
	}
}

func (s *inspectionScreen) leave(api.Message, *api.Message) {}

// inspectModelRequest reports every protected value the request would carry.
func inspectModelRequest(ctx context.Context, matcher *secretmatch.Matcher, req modelcall.CompletionRequest) []locatedModelSecret {
	screen := &inspectionScreen{ctx: ctx, matcher: matcher}
	walkModelRequest(screen, req)
	return screen.hits
}

// Inspection and redaction walk the same request fields.
func walkModelRequest(pass requestPass, req modelcall.CompletionRequest) modelcall.CompletionRequest {
	out := req
	out.Messages = append([]api.Message(nil), req.Messages...)
	for i := range out.Messages {
		source := out.Messages[i]
		pass.enter(source)
		out.Messages[i] = (&messageRedactor{screen: pass}).message(source)
		pass.leave(source, &out.Messages[i])
	}
	out.Tools = append([]tools.ToolMeta(nil), req.Tools...)
	for i := range out.Tools {
		definitions := &messageRedactor{
			screen: pass,
			origin: fieldOrigin{kind: secretmatch.SourceToolDefinition, tool: out.Tools[i].Name},
		}
		out.Tools[i].Description = definitions.text("description", "", out.Tools[i].Description)
		out.Tools[i].ArgsSchema = definitions.stringMap("args_schema", out.Tools[i].ArgsSchema)
	}
	return out
}

func sourceKindForMessage(msg api.Message) secretmatch.SourceKind {
	switch msg.Role {
	case api.MessageRoleSystem:
		return secretmatch.SourceSystemMessage
	case api.MessageRoleAssistant:
		return secretmatch.SourceAssistantMessage
	case api.MessageRoleTool:
		return secretmatch.SourceToolResult
	default:
		return secretmatch.SourceUserMessage
	}
}

func sourceForMessage(msg api.Message) (tool, path, callID string, source gate.SecretSource) {
	source = gate.SecretSourceUnknown
	if msg.ToolResult == nil {
		return "", "", "", source
	}
	tool = strings.TrimSpace(msg.ToolResult.Tool)
	callID = strings.TrimSpace(msg.ToolResult.ToolCallID)
	if value, ok := msg.ToolResult.ToolArgs["path"].(string); ok {
		path = strings.TrimSpace(value)
	}
	// Completed native fetches are host-observed public inbound flows.
	if tool == string(secretmatch.SurfaceFetchURL) && msg.ToolResult.Outcome == api.ToolResultOutcomeCompleted {
		source = gate.SecretSourcePublicInbound
	}
	return tool, path, callID, source
}

func modelOriginKind(path string) secretmatch.OriginKind {
	if strings.TrimSpace(path) != "" {
		return secretmatch.OriginFile
	}
	return secretmatch.OriginField
}

func primaryModelSecret(hits []locatedModelSecret) locatedModelSecret {
	primary := hits[0]
	for _, hit := range hits[1:] {
		// Disclosure direction outranks severity when choosing the card subject.
		primaryPublic := primary.source == gate.SecretSourcePublicInbound
		hitPublic := hit.source == gate.SecretSourcePublicInbound
		if primaryPublic && !hitPublic || primaryPublic == hitPublic &&
			secretmatch.SeverityRank(hit.match.Severity) > secretmatch.SeverityRank(primary.match.Severity) {
			primary = hit
		}
	}
	return primary
}

// runeSlice returns the text a match covered, in rune coordinates.
func runeSlice(text string, start, end int) string {
	runes := []rune(text)
	if start < 0 || end > len(runes) || end <= start {
		return ""
	}
	return string(runes[start:end])
}

// rememberModelSecrets adds confirmed values to exact-match screening.
func rememberModelSecrets(matcher *secretmatch.Matcher, rootSessionID string, hits []locatedModelSecret) {
	if matcher == nil || strings.TrimSpace(rootSessionID) == "" {
		return
	}
	values := make([]secretmatch.Remembered, 0, len(hits))
	for _, hit := range hits {
		if hit.value == "" || hit.match.Source != secretmatch.SourceShapeRule {
			continue
		}
		values = append(values, secretmatch.Remembered{
			Secret: hit.value,
			Name:   hit.label,
			Origin: string(hit.sourceKind),
			RuleID: hit.match.RuleID,
			Title:  hit.match.Title,
			Source: secretmatch.SourceRememberedMatch,
		})
	}
	matcher.Remember(rootSessionID, values)
}

func physicalLineAtRune(text string, offset int) int {
	if offset < 0 {
		return 0
	}
	runes := []rune(text)
	if offset > len(runes) {
		return 0
	}
	return 1 + strings.Count(string(runes[:offset]), "\n")
}

// redactModelRequest writes live managed values as references and matches
// accepted by keep as placeholders, then states what it replaced.
func redactModelRequest(
	ctx context.Context,
	matcher *secretmatch.Matcher,
	req modelcall.CompletionRequest,
	keep func(secretmatch.Match) bool,
) modelcall.CompletionRequest {
	screen := &redactionScreen{ctx: ctx, matcher: matcher, keep: keep}
	out := walkModelRequest(screen, req)
	out.Messages = appendHostSecretRedactionNotice(out.Messages, screen.definitions)
	return out
}

// RedactMessageForStorage returns a detached, storage-safe message copy. The
// context carries session-attributed evidence.
func RedactMessageForStorage(ctx context.Context, matcher *secretmatch.Matcher, msg api.Message) (api.Message, bool) {
	if matcher == nil || matcher.Inert() {
		return msg, false
	}
	ctx = matcher.WithClassifications(ctx, matcher.ClassificationSnapshot(ctx))
	out, written := redactMessageFields(ctx, matcher, msg, nil)
	return out, written > 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
