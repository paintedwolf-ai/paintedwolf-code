package hitl

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
)

// ApprovalStage names the enforcement boundary currently holding the action.
type ApprovalStage string

const (
	ApprovalStagePreSpawn ApprovalStage = "pre_spawn"
	ApprovalStagePreDial  ApprovalStage = "pre_dial"
	ApprovalStagePreSend  ApprovalStage = "pre_send"
)

// ApprovalSubjectKind is the typed unit the human is reviewing.
type ApprovalSubjectKind string

const (
	ApprovalSubjectAction          ApprovalSubjectKind = "action"
	ApprovalSubjectActionSet       ApprovalSubjectKind = "action_set"
	ApprovalSubjectSocketSet       ApprovalSubjectKind = "socket_set"
	ApprovalSubjectDirectIP        ApprovalSubjectKind = "direct_ip"
	ApprovalSubjectProcessControl  ApprovalSubjectKind = "process_control"
	ApprovalSubjectHostExecution   ApprovalSubjectKind = "host_execution"
	ApprovalSubjectLocalListen     ApprovalSubjectKind = "local_listen"
	ApprovalSubjectLoopbackConnect ApprovalSubjectKind = "loopback_connect"
	ApprovalSubjectDestinationSet  ApprovalSubjectKind = "destination_set"
	ApprovalSubjectWriteRootSet    ApprovalSubjectKind = "write_root_set"
	ApprovalSubjectReadPathSet     ApprovalSubjectKind = "read_path_set"
	ApprovalSubjectSecret          ApprovalSubjectKind = "secret"
	ApprovalSubjectPackageSet      ApprovalSubjectKind = "package_set"
)

// approvalSubjectKinds is the closed reviewed-unit vocabulary.
var approvalSubjectKinds = []ApprovalSubjectKind{
	ApprovalSubjectAction, ApprovalSubjectActionSet, ApprovalSubjectSocketSet, ApprovalSubjectDirectIP, ApprovalSubjectProcessControl, ApprovalSubjectHostExecution,
	ApprovalSubjectLocalListen, ApprovalSubjectLoopbackConnect, ApprovalSubjectDestinationSet,
	ApprovalSubjectWriteRootSet, ApprovalSubjectReadPathSet, ApprovalSubjectSecret,
	ApprovalSubjectPackageSet,
}

const secretGenericShapeDetail = "generic_shape"

// secretScreeningGapDetail names content the secret screen could not read.
const secretScreeningGapDetail = "screening_gap"

// ApprovalTarget is one canonical member of the reviewed subject.
type ApprovalTarget struct {
	Kind    string         `json:"kind"`
	Label   string         `json:"label"`
	Details map[string]any `json:"details,omitempty"`
}

// ApprovalSubject is the single subject shared by card, authority and ledger.
type ApprovalSubject struct {
	Kind    ApprovalSubjectKind `json:"kind"`
	Title   string              `json:"title"`
	Summary string              `json:"summary,omitempty"`
	Targets []ApprovalTarget    `json:"targets"`
}

// ApprovalPresentation is reviewed host copy attached to the plan.
type ApprovalPresentation struct {
	IgnoreCandidateID string                      `json:"ignore_candidate_id,omitempty"`
	FileChanges       []api.ApprovalFileChange    `json:"file_changes,omitempty"`
	Action            string                      `json:"action"`
	Tool              string                      `json:"tool,omitempty"`
	Command           string                      `json:"command,omitempty"`
	Location          *api.ApprovalSecretLocation `json:"location,omitempty"`
	Impact            string                      `json:"impact"`
	Who               string                      `json:"who,omitempty"`
	IfWrong           string                      `json:"if_wrong,omitempty"`
	AllowLine         string                      `json:"allow_line,omitempty"`
	// Lead highlights one fact from the primary gate's citations.
	Lead string `json:"lead,omitempty"`
	// Gate names the primary reason for the card.
	Gate api.ApprovalGate `json:"gate,omitempty"`
	// Cited contains redaction-safe facts in gate-priority order.
	Cited []PresentedFact `json:"cited,omitempty"`
	// OptionNote explains why the redacted option is unavailable.
	OptionNote string `json:"option_note,omitempty"`

	GrantDelta      string              `json:"grant_delta,omitempty"`
	ConsequenceBand string              `json:"consequence_band,omitempty"`
	ConsequenceCode string              `json:"consequence_code,omitempty"`
	Detection       *DetectionMatch     `json:"detection,omitempty"`
	ApprovalRules   []ApprovalRuleMatch `json:"approval_rules,omitempty"`
}

// ApprovalOptionKind distinguishes current-action and reusable choices.
type ApprovalOptionKind string

const (
	ApprovalOptionCurrentAction ApprovalOptionKind = "current_action"
	ApprovalOptionLease         ApprovalOptionKind = "lease"
	ApprovalOptionRedacted      ApprovalOptionKind = "redacted"
	ApprovalOptionQuiet         ApprovalOptionKind = "quiet"
	ApprovalOptionTracked       ApprovalOptionKind = "tracked"
)

// ApprovalOptionRung orders choices independently from authority scope.
type ApprovalOptionRung string

const (
	ApprovalRungRedacted  ApprovalOptionRung = "redacted"
	ApprovalRungTracked   ApprovalOptionRung = "tracked"
	ApprovalRungUnchanged ApprovalOptionRung = "unchanged"
	ApprovalRungOnce      ApprovalOptionRung = "once"
	ApprovalRungDay       ApprovalOptionRung = "day"
	ApprovalRungChat      ApprovalOptionRung = "chat"
	ApprovalRungProject   ApprovalOptionRung = "project"
	ApprovalRungDevice    ApprovalOptionRung = "device"
)

// approvalRungOrder is the ladder in ordering-key order.
var approvalRungOrder = []ApprovalOptionRung{
	ApprovalRungTracked, ApprovalRungRedacted, ApprovalRungUnchanged, ApprovalRungOnce, ApprovalRungDay,
	ApprovalRungChat, ApprovalRungProject, ApprovalRungDevice,
}

// ApprovalOptionDecision is the outcome bound to a selected option.
type ApprovalOptionDecision string

const (
	ApprovalOptionApprove ApprovalOptionDecision = "approve"
	ApprovalOptionRedact  ApprovalOptionDecision = "redact"
	ApprovalOptionTrack   ApprovalOptionDecision = "track"
)

// AuthorityDeltaKind is the closed set of authority an option can install.
type AuthorityDeltaKind string

const (
	AuthorityCurrentAction  AuthorityDeltaKind = "current_action"
	AuthorityGenericGrant   AuthorityDeltaKind = "generic_grant"
	AuthoritySocketPermit   AuthorityDeltaKind = "socket_permit"
	AuthoritySocketChat     AuthorityDeltaKind = "socket_chat"
	AuthorityDirectIPPermit AuthorityDeltaKind = "direct_ip_permit"
	AuthorityDirectIPChat   AuthorityDeltaKind = "direct_ip_chat"
	AuthorityWriteRootChat  AuthorityDeltaKind = "write_root_chat"
	// AuthorityReadPathChat grants protected-path reads to one chat.
	AuthorityReadPathChat AuthorityDeltaKind = "read_path_chat"
	// AuthorityLocalListenChat grants local listener access to one chat.
	AuthorityLocalListenChat AuthorityDeltaKind = "local_listen_chat"
	// AuthorityLoopbackConnectChat grants local connections to one chat.
	AuthorityLoopbackConnectChat AuthorityDeltaKind = "loopback_connect_chat"
	// AuthorityGrantedPath widens native tool access without changing confinement.
	AuthorityGrantedPath AuthorityDeltaKind = "granted_path"
	// AuthorityAskQuiet suppresses one ask key without satisfying its gate.
	AuthorityAskQuiet AuthorityDeltaKind = "ask_quiet"
	// AuthorityTrustDestination records the device-wide provider trust that
	// Settings → AI providers records, bound to the resolved destination.
	AuthorityTrustDestination AuthorityDeltaKind = "trust_destination"
)

// TrustDestinationDelta names the provider instance and the resolved
// destination identity a trust option binds to.
type TrustDestinationDelta struct {
	ProviderID    string `json:"provider_id"`
	DestinationID string `json:"destination_id"`
	Label         string `json:"label"`
}

// ApprovalSocketTarget is the canonical approved/resolved socket pair.
type ApprovalSocketTarget struct {
	ApprovedPath string `json:"approved_path"`
	ResolvedPath string `json:"resolved_path"`
}

// ApprovalAuthorityDelta is authority installed before action release.
type ApprovalAuthorityDelta struct {
	Kind  AuthorityDeltaKind `json:"kind"`
	Grant *ApprovalGrant     `json:"grant,omitempty"`
	// ChatSessionID is the chat that chat-lifetime authority belongs to.
	ChatSessionID string                 `json:"chat_session_id,omitempty"`
	SessionID     string                 `json:"session_id,omitempty"`
	ToolCallID    string                 `json:"tool_call_id,omitempty"`
	ActionDigest  string                 `json:"action_digest,omitempty"`
	Sockets       []ApprovalSocketTarget `json:"sockets,omitempty"`
	DirectIPLease *DirectIPLease         `json:"direct_ip_lease,omitempty"`
	WriteRoots    []string               `json:"write_roots,omitempty"`
	// ReadPaths carries protected-path read grants; separate from write roots.
	ReadPaths []string `json:"read_paths,omitempty"`
	// ListenPorts narrows a local-listen grant to exact local ports; empty means any.
	ListenPorts []uint16 `json:"listen_ports,omitempty"`
	// ConnectPorts narrows local outbound connections; empty means any local port.
	ConnectPorts []uint16 `json:"connect_ports,omitempty"`
	// GrantedPath carries the approved filesystem access.
	GrantedPath *GrantedPathDelta `json:"granted_path,omitempty"`
	// AskQuiet carries the quiet key/label installed by a quiet option.
	AskQuiet *AskQuietDelta `json:"ask_quiet,omitempty"`
	// TrustDestination carries the provider trust installed by a trust option.
	TrustDestination *TrustDestinationDelta `json:"trust_destination,omitempty"`
	TTLSeconds       int                    `json:"ttl_seconds,omitempty"`
}

// ChatSession returns the chat that chat-scoped authority belongs to.
func (d ApprovalAuthorityDelta) ChatSession() string {
	return d.ChatSessionID
}

// AskQuietDelta is the ask-suppression payload for AuthorityAskQuiet.
type AskQuietDelta struct {
	ElevatedEffects []api.ElevatedAccessEffect `json:"elevated_effects,omitempty"`
	ID              string                     `json:"id"`
	Key             string                     `json:"key"`
	Label           string                     `json:"label"`
}

// GrantedPathDelta is one filesystem access an approval installs.
type GrantedPathDelta struct {
	// Path is absolute.
	Path string `json:"path" yaml:"path"`
	// Write distinguishes mutation authority from read authority.
	Write bool `json:"write,omitempty" yaml:"write,omitempty"`
	// Tree covers Path and every descendant. Exact grants cover Path only.
	Tree bool `json:"tree,omitempty" yaml:"tree,omitempty"`
}

// ApprovalOption is one exact affirmative choice on a plan.
type ApprovalOption struct {
	ID    string             `json:"id"`
	Kind  ApprovalOptionKind `json:"kind"`
	Rung  ApprovalOptionRung `json:"rung"`
	Scope ApprovalGrantScope `json:"scope,omitempty"`
	// Group names a second ladder on a two-subject card. Empty is the primary.
	Group          string `json:"group,omitempty"`
	DirectoryScope string `json:"directory_scope,omitempty"`
	Title          string `json:"title"`
	Coverage       string `json:"coverage"`
	ExpiresWhen    string `json:"expires_when"`
	ReaskWhen      string `json:"reask_when"`
	// DecisionAction controls release or redaction after resolution.
	DecisionAction ApprovalOptionDecision `json:"decision_action"`
	// Disabled preserves the option position and displays Note as its reason.
	Disabled  bool                     `json:"disabled,omitempty"`
	Note      string                   `json:"note,omitempty"`
	Authority []ApprovalAuthorityDelta `json:"authority"`
}

// FaceContext carries the facts the recommended option depends on.
type FaceContext struct {
	SecretManaged bool
}

// ApprovalPlan is the immutable authority contract for one approval.
type ApprovalPlan struct {
	ID                  string               `json:"id"`
	ActionDigest        string               `json:"action_digest"`
	Stage               ApprovalStage        `json:"stage"`
	Subject             ApprovalSubject      `json:"subject"`
	Presentation        ApprovalPresentation `json:"presentation"`
	Reasons             []api.ApprovalGate   `json:"reasons"`
	Options             []ApprovalOption     `json:"options"`
	DirectoryScopes     []string             `json:"directory_scopes,omitempty"`
	RecommendedOptionID string               `json:"recommended_option_id"`
	// Held names person-held values an approving option would send; while
	// their chat is locked, each such answer needs the person's verified
	// presence, which unlocks it.
	Held *HeldRelease `json:"held,omitempty"`
}

// NewApprovalPlan validates, orders, and selects the recommended option.
func NewApprovalPlan(action ProposedAction, stage ApprovalStage, subject ApprovalSubject, presentation ApprovalPresentation, reasons []api.ApprovalGate, options []ApprovalOption, face FaceContext) (*ApprovalPlan, error) {
	digest := GrantKey(action)
	if digest == "" {
		return nil, fmt.Errorf("approval action identity cannot be encoded")
	}
	sorted := sortApprovalOptions(attachQuietGrantAuthority(options))
	compactReasons := compactGates(reasons)
	faceID, err := computeRecommendedOptionID(subject.Kind, compactReasons, sorted, face)
	if err != nil {
		return nil, err
	}
	candidate := ApprovalPlan{
		ActionDigest:        digest,
		Stage:               stage,
		Subject:             subject,
		Presentation:        presentation,
		Reasons:             compactReasons,
		Options:             sorted,
		DirectoryScopes:     approvalDirectoryScopes(options),
		RecommendedOptionID: faceID,
	}
	id, err := candidate.canonicalID()
	if err != nil {
		return nil, err
	}
	candidate.ID = id
	// Canonical round-tripping detaches caller-supplied references.
	stored, err := json.Marshal(candidate)
	if err != nil {
		return nil, fmt.Errorf("copy approval plan: %w", err)
	}
	var plan ApprovalPlan
	if err := json.Unmarshal(stored, &plan); err != nil {
		return nil, fmt.Errorf("copy approval plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return &plan, nil
}

func (p ApprovalPlan) canonicalID() (string, error) {
	p.ID = ""
	canonical, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("canonical approval plan: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "approval_plan_" + base64.RawURLEncoding.EncodeToString(sum[:12]), nil
}

func (p ApprovalPlan) Option(id string) (ApprovalOption, bool) {
	id = strings.TrimSpace(id)
	for _, option := range p.Options {
		if option.ID == id {
			return option, true
		}
	}
	return ApprovalOption{}, false
}

// CurrentActionOption is the explicit one-shot option used by ordinary cards.
func CurrentActionOption() ApprovalOption {
	return ApprovalOption{
		ID: "approve_current_action", Kind: ApprovalOptionCurrentAction, Rung: ApprovalRungOnce,
		Title: TitleAllowOnce, Coverage: CoverageOnlyThisExactAction,
		ExpiresWhen: ExpiresAfterThisAction, ReaskWhen: ReaskWhenActionRunsAgain,
		DecisionAction: ApprovalOptionApprove,
		Authority:      []ApprovalAuthorityDelta{{Kind: AuthorityCurrentAction}},
	}
}

// SendUnchangedOption is the secret-card one-shot that sends the payload as-is.
func SendUnchangedOption() ApprovalOption {
	option := CurrentActionOption()
	option.ID = "send_unchanged"
	option.Rung = ApprovalRungUnchanged
	option.Title = TitleSendUnchanged
	return option
}

// secretSendOption is the single release. A managed value reads Send; a raw
// detection reads Send unchanged.
func secretSendOption(managed bool) ApprovalOption {
	option := SendUnchangedOption()
	if managed {
		option.Title = TitleSend
	}
	return option
}

// SendRedactedOption is the one-send redaction. It sits in the Redaction group
// after the shared ladder, so the five slots every card has stay in place.
func SendRedactedOption() ApprovalOption {
	return ApprovalOption{
		ID: "send_redacted", Kind: ApprovalOptionRedacted, Rung: ApprovalRungRedacted, Group: GroupRedaction,
		Title: TitleSendRedacted, Coverage: CoverageEveryCredentialReplaced,
		ExpiresWhen: ExpiresAfterThisAction, ReaskWhen: ReaskWhenActionRunsAgain,
		DecisionAction: ApprovalOptionRedact,
		Authority:      []ApprovalAuthorityDelta{{Kind: AuthorityCurrentAction}},
	}
}

// BreakingSendRedactedOption is the one-send redaction on a seam where the
// value authenticates the call.
func BreakingSendRedactedOption() ApprovalOption {
	option := SendRedactedOption()
	option.Coverage = CoverageRedactionBreaks
	return option
}

// TrackAndReplaceOption stores detected values and sends managed references.
func TrackAndReplaceOption() ApprovalOption {
	return ApprovalOption{
		ID: "track_and_replace", Kind: ApprovalOptionTracked, Rung: ApprovalRungTracked,
		Title: TitleProtect, Coverage: CoverageProtectedAndSent,
		ExpiresWhen: ExpiresAfterThisAction, ReaskWhen: "the request contains a different untracked credential",
		DecisionAction: ApprovalOptionTrack,
		Authority:      []ApprovalAuthorityDelta{{Kind: AuthorityCurrentAction}},
	}
}

// UnavailableSendRedactedOption marks redaction as unavailable, in place.
func UnavailableSendRedactedOption(note string) ApprovalOption {
	option := SendRedactedOption()
	option.Disabled = true
	option.Note = firstNonEmpty(note, CoverageRedactionUnavailable)
	option.Coverage = CoverageRedactionUnavailable
	return option
}

// GrantOption projects an existing host-minted lease into a plan option.
func GrantOption(offer ApprovalGrantOffer) ApprovalOption {
	authority := offer.Authority
	if len(authority) == 0 {
		authority = []ApprovalAuthorityDelta{{Kind: AuthorityGenericGrant, Grant: &offer.Grant, TTLSeconds: offer.TTLSeconds}}
	}
	return ApprovalOption{
		ID: offer.ID, Kind: ApprovalOptionLease, Rung: offer.Rung, Scope: offer.Scope,
		Group: offer.Group, DirectoryScope: offer.DirectoryScope, Title: offer.Title, Coverage: offer.Coverage,
		ExpiresWhen: offer.ExpiresWhen, ReaskWhen: offer.ReaskWhen,
		DecisionAction: ApprovalOptionApprove,
		Disabled:       offer.Disabled, Note: offer.Note,
		Authority: authority,
	}
}

// ContinuingLeaseOption authorizes both the held action and subsequent matching actions.
func ContinuingLeaseOption(offer ApprovalGrantOffer, continuing ...ApprovalAuthorityDelta) ApprovalOption {
	option := GrantOption(offer)
	if len(continuing) == 0 {
		return option
	}
	option.Authority = append(append([]ApprovalAuthorityDelta(nil), continuing...), option.Authority...)
	return option
}

// compactGates deduplicates gates without changing priority.
func compactGates(in []api.ApprovalGate) []api.ApprovalGate {
	out := make([]api.ApprovalGate, 0, len(in))
	seen := map[api.ApprovalGate]struct{}{}
	for _, value := range in {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func storeApprovalPlan(plan *ApprovalPlan) (map[string]any, error) {
	if plan == nil {
		return nil, fmt.Errorf("approval plan required")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	return stored, nil
}

func approvalPlanFromMap(raw map[string]any) (*ApprovalPlan, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var plan ApprovalPlan
	if err := json.Unmarshal(encoded, &plan); err != nil {
		return nil, err
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return &plan, nil
}

// PresentedFact is a redaction-safe cited input rendered on a card.
type PresentedFact struct {
	Gate   api.ApprovalGate `json:"gate,omitempty"`
	Key    string           `json:"key"`
	Value  string           `json:"value"`
	Source string           `json:"source"`
}

// presentFacts projects gate citations for the wire.
func presentFacts(facts []gate.Fact) []PresentedFact {
	if len(facts) == 0 {
		return nil
	}
	out := make([]PresentedFact, 0, len(facts))
	for _, f := range facts {
		out = append(out, PresentedFact{Gate: f.Gate, Key: f.Key, Value: f.Value, Source: f.Source})
	}
	return out
}

// PresentDecision projects a gate decision into plan fields.
func PresentDecision(decision *gate.Decision) (api.ApprovalGate, []PresentedFact, []api.ApprovalGate) {
	if decision == nil {
		return "", nil, nil
	}
	return decision.Primary, presentFacts(decision.Cited), decision.Gates()
}
