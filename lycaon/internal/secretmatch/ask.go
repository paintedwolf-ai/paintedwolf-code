package secretmatch

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
)

// ScreenSurface names which host-mediated outbound seam raised the ask.
type ScreenSurface string

const (
	SurfaceWebSearch   ScreenSurface = "web_search"
	SurfaceFetchURL    ScreenSurface = "fetch_url"
	SurfaceHTTPRequest ScreenSurface = "http_request"
	SurfaceMCP         ScreenSurface = "mcp"
	SurfaceModel       ScreenSurface = "model_request"
	SurfaceVisualModel ScreenSurface = "visual_perception"
	SurfaceCommand     ScreenSurface = "command"
	SurfaceTerminal    ScreenSurface = "terminal"
	SurfaceFile        ScreenSurface = "file"
)

// DestinationKind names who receives the value on the other side of a seam.
type DestinationKind string

const (
	// DestinationModelProvider is the configured AI provider; the model reads it.
	DestinationModelProvider DestinationKind = "model_provider"
	// DestinationService is an external service the host addresses by URL.
	DestinationService DestinationKind = "service"
	// DestinationProcess is a local process the host starts; where it sends
	// the value afterwards is not observed here.
	DestinationProcess DestinationKind = "process"
	// DestinationFile is a local file on disk.
	DestinationFile DestinationKind = "file"
)

// surfaceFact defines presentation, redaction support, and who receives the send.
type surfaceFact struct {
	label       string
	noRedaction string
	destination DestinationKind
	// redactionBreaks marks a seam where the value authenticates the call, so
	// stripping it fails the request rather than weakening it.
	redactionBreaks bool
}

const argvNoRedaction = "Redaction is not offered here: replacing the value would run a command neither you nor the model wrote."

// surfaceFacts is the closed presentation table for screen surfaces.
var surfaceFacts = map[ScreenSurface]surfaceFact{
	SurfaceWebSearch:   {label: "web search", destination: DestinationService, redactionBreaks: true},
	SurfaceFetchURL:    {label: "page fetch", destination: DestinationService, redactionBreaks: true},
	SurfaceHTTPRequest: {label: "HTTP request", destination: DestinationService, redactionBreaks: true},
	SurfaceMCP:         {label: "MCP request", destination: DestinationService, redactionBreaks: true},
	// A model request still completes without the value, so redaction does not
	// break it.
	SurfaceModel:       {label: "model request", destination: DestinationModelProvider},
	SurfaceVisualModel: {label: "visual perception", destination: DestinationModelProvider},
	SurfaceCommand:     {label: "command", noRedaction: argvNoRedaction, destination: DestinationProcess},
	SurfaceTerminal:    {label: "terminal input", noRedaction: argvNoRedaction, destination: DestinationProcess},
	SurfaceFile:        {label: "local file", noRedaction: "The file requires the secret value to function.", destination: DestinationFile, redactionBreaks: true},
}

// Label returns the display name or the surface identifier.
func (s ScreenSurface) Label() string {
	if fact, ok := surfaceFacts[s]; ok {
		return fact.label
	}
	return string(s)
}

// CanRedact reports whether the surface supports safe rewriting.
func (s ScreenSurface) CanRedact() bool {
	fact, ok := surfaceFacts[s]
	return ok && fact.noRedaction == ""
}

// DestinationKind reports who receives a send over this surface.
func (s ScreenSurface) DestinationKind() DestinationKind {
	if fact, ok := surfaceFacts[s]; ok && fact.destination != "" {
		return fact.destination
	}
	return DestinationService
}

// RedactionBreaksRequest reports whether removing the value fails the call
// rather than degrading it.
func (s ScreenSurface) RedactionBreaksRequest() bool {
	fact, ok := surfaceFacts[s]
	return ok && fact.redactionBreaks
}

// RedactionNote explains why redaction is unavailable.
func (s ScreenSurface) RedactionNote() string {
	fact, ok := surfaceFacts[s]
	if !ok {
		return "Redaction is not offered here: this destination has no reviewed way to rewrite the request."
	}
	return fact.noRedaction
}

// Decision is a human resolution for an outbound secret hit.
type Decision string

const (
	// Unanswered means no answer arrived before the card closed.
	Unanswered Decision = "unanswered"
	// Withhold records a declined send.
	Withhold        Decision = "withhold"
	SendUnchanged   Decision = "send_unchanged"
	SendRedacted    Decision = "send_redacted"
	TrackAndReplace Decision = "track_and_replace"
)

// Blocks reports whether the matched value stays local.
func (d Decision) Blocks() bool {
	return d != SendUnchanged && d != SendRedacted && d != TrackAndReplace
}

// Fault stages identify where a card failed.
const (
	// FaultStageCheckpointsUnwired means no checkpoint manager is available.
	FaultStageCheckpointsUnwired = "checkpoints_unwired"
	// FaultStageNoSession means the alert has no session.
	FaultStageNoSession = "no_session"
	// FaultStageRaise means card creation or waiting failed.
	FaultStageRaise = "raise"
	// FaultStageRedactUnsupported means the surface cannot rewrite content.
	FaultStageRedactUnsupported = "redact_unsupported"
	// FaultStageScreenUnwired means the screen has no ask handler.
	FaultStageScreenUnwired = "screen_unwired"
	// FaultStageManagedSecretStore means managed values could not be loaded or stored.
	FaultStageManagedSecretStore = "managed_secret_store"
)

// AskFault reports a failure before a card reaches a human.
type AskFault struct {
	// Stage identifies the failed host step.
	Stage string
	Err   error
}

func (f *AskFault) Error() string {
	if f == nil {
		return "outbound secret screen could not ask"
	}
	msg := "outbound secret screen could not ask (" + f.Stage + ")"
	if f.Err != nil {
		msg += ": " + f.Err.Error()
	}
	return msg
}

func (f *AskFault) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Err
}

// Faulted reports whether err is a screen fault rather than a human answer.
func Faulted(err error) (*AskFault, bool) {
	var fault *AskFault
	if errors.As(err, &fault) {
		return fault, true
	}
	return nil, false
}

// NewAskFault names the host step that could not put the card to a human.
func NewAskFault(stage string, err error) error {
	return &AskFault{Stage: strings.TrimSpace(stage), Err: err}
}

// SourceKind identifies the exact outbound field that matched.
type SourceKind string

const (
	SourceSystemMessage    SourceKind = "system_message"
	SourceUserMessage      SourceKind = "user_message"
	SourceAssistantMessage SourceKind = "assistant_message"
	SourceToolResult       SourceKind = "tool_result"
	SourceToolCall         SourceKind = "tool_call"
	SourceToolDefinition   SourceKind = "tool_definition"
	SourceToolArgument     SourceKind = "tool_argument"
	SourceVisualCapture    SourceKind = "visual_capture"
)

// OriginKind says whether SourcePath is a project file or an outbound field.
type OriginKind string

const (
	OriginFile  OriginKind = "file"
	OriginField OriginKind = "field"
)

// Alert is the redaction-safe summary for an outbound-secret card.
type Alert struct {
	// ReviewValue is ephemeral and never enters checkpoint or capture serialization.
	ReviewValue      string `json:"-"`
	SessionID        string
	RootSessionID    string
	ProjectID        string
	ProjectDir       string
	ToolCallID       string
	Surface          ScreenSurface
	DestinationID    string
	DestinationLabel string
	// Recipients adds explicitly requested service handoffs to this invocation.
	Recipients  []Recipient
	SecretNames []string
	// ConnectPorts comes from the parsed service declaration.
	ConnectPorts []uint16
	// ProviderID enables the configured provider's trust option on model requests.
	ProviderID string
	// DestinationTrusted is a standing device-scope decision bound to the
	// resolved transport identity; a repointed provider is not trusted.
	DestinationTrusted bool
	// HostComposed sends redact matched values automatically.
	HostComposed bool
	// ChatGenerated and RecipientsLocal reach the gate as facts; posture
	// decides whether a chat's generated secrets may reach local recipients.
	ChatGenerated   bool
	RecipientsLocal bool
	RuleID          string
	RuleTitle       string
	GenericShape    string
	Occurrences     int
	SourceKind      SourceKind
	SourceTool      string
	SourcePath      string
	SourceLine      int
	// OriginKind is file (SourcePath is a project path) or field (argument/slot).
	OriginKind OriginKind
	// SourceToolCallID is the originating tool row; ToolCallID is the held send.
	SourceToolCallID string
	// CommandLine is a host-redacted argv for command/terminal cards.
	CommandLine string
	// Fingerprints are host-only secret identities.
	Fingerprints []SecretFingerprint
	// Source is the observed disclosure direction.
	Source gate.SecretSource
	// VarName and Container carry harvest evidence from the primary match.
	VarName   string
	Container string
	// ContestToken is the receipt the agent cited to ask for the real value.
	ContestToken string
	// ScreeningGap marks content the screen could not read; redaction cannot
	// be honoured for it.
	ScreeningGap ScreeningGap
}

// CanRedact reports whether this alert's surface and coverage allow a
// redacted send.
func (a Alert) CanRedact() bool {
	return a.Surface.CanRedact() && a.ScreeningGap == ""
}

// RedactionNote explains why redaction is unavailable for this alert.
func (a Alert) RedactionNote() string {
	if a.ScreeningGap != "" {
		return a.ScreeningGap.RedactionNote()
	}
	return a.Surface.RedactionNote()
}

// Managed reports whether this alert cites exact managed-capability evidence.
func (a Alert) Managed() bool { return IsManagedRule(a.RuleID) }

// Resolution carries the send mode and optional user guidance.
type Resolution struct {
	Decision Decision
	Guidance string
	// ReceiptToken authorizes one redaction contest.
	ReceiptToken string
}

// AskFunc resolves an outbound secret hit or returns an AskFault.
type AskFunc func(ctx context.Context, alert Alert) (Resolution, error)

// AskAttribution carries checkpoint identity.
type AskAttribution struct {
	SessionID     string
	RootSessionID string
	ProjectID     string
	ProjectDir    string
	ToolCallID    string
}

type askAttrKey struct{}

// WithAskAttribution attaches session fields for AskFunc implementations.
func WithAskAttribution(ctx context.Context, attr AskAttribution) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, askAttrKey{}, attr)
}

// AskAttributionFrom returns attached attribution.
func AskAttributionFrom(ctx context.Context) AskAttribution {
	if ctx == nil {
		return AskAttribution{}
	}
	attr, _ := ctx.Value(askAttrKey{}).(AskAttribution)
	return attr
}
