package hitl

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/secretmatch"
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
	Group       string `json:"group,omitempty"`
	Title       string `json:"title"`
	Coverage    string `json:"coverage"`
	ExpiresWhen string `json:"expires_when"`
	ReaskWhen   string `json:"reask_when"`
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

// Validate enforces the invariants that make the plan safe to render and apply.
func (p ApprovalPlan) Validate() error {
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.ActionDigest) == "" {
		return fmt.Errorf("approval plan identity is incomplete")
	}
	if err := p.validateHeld(); err != nil {
		return err
	}
	if p.Stage == "" || p.Subject.Kind == "" || strings.TrimSpace(p.Subject.Title) == "" || len(p.Subject.Targets) == 0 {
		return fmt.Errorf("approval plan subject is incomplete")
	}
	if !validApprovalStage(p.Stage) || !validApprovalSubjectKind(p.Subject.Kind) {
		return fmt.Errorf("approval plan stage or subject kind is unknown")
	}
	if !approvalSubjectAllowedAtStage(p.Subject.Kind, p.Stage) {
		return fmt.Errorf("approval plan subject %q is invalid at stage %q", p.Subject.Kind, p.Stage)
	}
	for _, target := range p.Subject.Targets {
		if strings.TrimSpace(target.Kind) == "" || strings.TrimSpace(target.Label) == "" {
			return fmt.Errorf("approval plan target is incomplete")
		}
		if p.Subject.Kind == ApprovalSubjectSecret {
			// A secret target names the matched value's shape or the content
			// the screen could not read.
			shape, _ := target.Details[secretGenericShapeDetail].(string)
			gap, _ := target.Details[secretScreeningGapDetail].(string)
			if strings.TrimSpace(shape) == "" && !secretmatch.ScreeningGap(gap).Valid() {
				return fmt.Errorf("secret approval plan target has no generic shape or screening gap")
			}
		}
	}
	if strings.TrimSpace(p.Presentation.Action) == "" || strings.TrimSpace(p.Presentation.Impact) == "" {
		return fmt.Errorf("approval plan presentation is incomplete")
	}
	if err := p.validateGateProvenance(); err != nil {
		return err
	}
	if len(p.Options) == 0 {
		return fmt.Errorf("approval plan has no affirmative option")
	}
	if strings.TrimSpace(p.RecommendedOptionID) == "" {
		return fmt.Errorf("approval plan has no recommended option id")
	}
	seenID := map[string]struct{}{}
	seenRung := map[string]struct{}{}
	var prevGroup string
	prevRungRank := -1
	faceFound := false
	redactionOffered := false
	redactionDisabled := false
	selectable := 0
	for i, option := range p.Options {
		if slices.Contains(p.Reasons, api.GateAgentPolicyChange) && option.Kind == ApprovalOptionQuiet {
			return fmt.Errorf("agent policy changes cannot be quieted")
		}
		id := strings.TrimSpace(option.ID)
		if id == "" || strings.TrimSpace(option.Title) == "" || strings.TrimSpace(option.Coverage) == "" || strings.TrimSpace(option.ExpiresWhen) == "" || strings.TrimSpace(option.ReaskWhen) == "" || len(option.Authority) == 0 {
			return fmt.Errorf("approval option %q is incomplete", id)
		}
		if !validApprovalRung(option.Rung) {
			return fmt.Errorf("approval option %q: unknown rung %q", id, option.Rung)
		}
		if !validApprovalGroup(strings.TrimSpace(option.Group)) {
			return fmt.Errorf("approval option %q: unknown group heading %q", id, option.Group)
		}
		if err := option.validateKind(); err != nil {
			return fmt.Errorf("approval option %q: %w", id, err)
		}
		if err := option.validateGrantIdentities(); err != nil {
			return fmt.Errorf("approval option %q: %w", id, err)
		}
		if !option.Disabled && !p.OptionContinues(option) {
			return fmt.Errorf("approval option %q cannot continue its held subject", id)
		}
		if option.Disabled {
			if strings.TrimSpace(option.Note) == "" {
				return fmt.Errorf("approval option %q is disabled without saying why", id)
			}
		} else {
			selectable++
		}
		if option.Kind == ApprovalOptionRedacted {
			if p.Subject.Kind != ApprovalSubjectSecret {
				return fmt.Errorf("approval option %q: redaction is only valid for a secret subject", id)
			}
			redactionOffered = true
			redactionDisabled = option.Disabled
		}
		if _, exists := seenID[id]; exists {
			return fmt.Errorf("duplicate approval option %q", id)
		}
		seenID[id] = struct{}{}
		groupKey := strings.TrimSpace(option.Group) + "\x00" + string(option.Rung)
		if _, exists := seenRung[groupKey]; exists {
			return fmt.Errorf("duplicate approval rung %q in group %q", option.Rung, option.Group)
		}
		seenRung[groupKey] = struct{}{}
		group := strings.TrimSpace(option.Group)
		rank := rungRank(option.Rung)
		if i > 0 {
			if groupOrder(group) < groupOrder(prevGroup) || (group == prevGroup && rank < prevRungRank) {
				return fmt.Errorf("approval options are not in (group, rung) order")
			}
		}
		prevGroup, prevRungRank = group, rank
		if id == p.RecommendedOptionID {
			faceFound = true
		}
		for _, delta := range option.Authority {
			if err := delta.validate(); err != nil {
				return fmt.Errorf("approval option %q: %w", id, err)
			}
		}
	}
	if !faceFound {
		return fmt.Errorf("recommended_option_id %q is not in options", p.RecommendedOptionID)
	}
	if selectable == 0 {
		return fmt.Errorf("approval plan has no selectable option")
	}
	if err := p.validateSecretFace(redactionOffered); err != nil {
		return err
	}
	if err := p.validateOptionNote(redactionOffered && !redactionDisabled); err != nil {
		return err
	}
	if err := p.validateSecretLocation(); err != nil {
		return err
	}
	expectedID, err := p.canonicalID()
	if err != nil {
		return err
	}
	if p.ID != expectedID {
		return fmt.Errorf("approval plan content does not match its identity")
	}
	return nil
}

// Credential redaction cannot be the default action because it may prevent the call from working.
func (p ApprovalPlan) validateSecretFace(redactionOffered bool) error {
	if p.Subject.Kind != ApprovalSubjectSecret {
		return nil
	}
	if !redactionOffered {
		return fmt.Errorf("secret approval plan has no redacted option")
	}
	face, ok := p.Option(p.RecommendedOptionID)
	if !ok {
		return fmt.Errorf("secret face is not in options")
	}
	if face.Disabled {
		return fmt.Errorf("secret face is disabled")
	}
	switch {
	case face.Kind == ApprovalOptionRedacted:
		return fmt.Errorf("redaction is never the secret face")
	case face.Kind == ApprovalOptionTracked:
		return nil
	case face.Kind == ApprovalOptionCurrentAction && face.Rung == ApprovalRungUnchanged:
		return nil
	case face.Kind == ApprovalOptionLease && face.Scope == ApprovalGrantScopeChat:
		return nil
	}
	return fmt.Errorf("secret face must be Protect, the single send, or the chat release")
}

// validateOptionNote requires an explanation when the redacted option is disabled.
func (p ApprovalPlan) validateOptionNote(redactionSelectable bool) error {
	note := strings.TrimSpace(p.Presentation.OptionNote)
	if p.Subject.Kind != ApprovalSubjectSecret {
		return nil
	}
	if redactionSelectable && note != "" {
		return fmt.Errorf("redaction note is set on a card that offers redaction")
	}
	if !redactionSelectable && note == "" {
		return fmt.Errorf("secret approval plan disables redaction without saying why")
	}
	return nil
}

func (p ApprovalPlan) validateGateProvenance() error {
	primary := p.Presentation.Gate
	if !gate.IsKnown(primary) || len(p.Presentation.Cited) == 0 || len(p.Reasons) == 0 || p.Reasons[0] != primary {
		return fmt.Errorf("approval plan gate provenance is incomplete")
	}
	reasons := make(map[api.ApprovalGate]struct{}, len(p.Reasons))
	for _, reason := range p.Reasons {
		if !gate.IsKnown(reason) {
			return fmt.Errorf("approval plan reason %q is not a gate", reason)
		}
		reasons[reason] = struct{}{}
	}
	cited := make(map[api.ApprovalGate]struct{}, len(reasons))
	for _, fact := range p.Presentation.Cited {
		if _, ok := reasons[fact.Gate]; !ok || strings.TrimSpace(fact.Key) == "" || strings.TrimSpace(fact.Value) == "" || strings.TrimSpace(fact.Source) == "" {
			return fmt.Errorf("approval plan citation has invalid gate provenance")
		}
		cited[fact.Gate] = struct{}{}
	}
	for reason := range reasons {
		if _, ok := cited[reason]; !ok {
			return fmt.Errorf("approval plan gate %q has no citation", reason)
		}
	}
	return nil
}

func validApprovalStage(stage ApprovalStage) bool {
	switch stage {
	case ApprovalStagePreSpawn, ApprovalStagePreDial, ApprovalStagePreSend:
		return true
	default:
		return false
	}
}

func validApprovalSubjectKind(kind ApprovalSubjectKind) bool {
	for _, candidate := range approvalSubjectKinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

func approvalSubjectAllowedAtStage(kind ApprovalSubjectKind, stage ApprovalStage) bool {
	switch kind {
	case ApprovalSubjectAction, ApprovalSubjectActionSet, ApprovalSubjectSocketSet, ApprovalSubjectPackageSet, ApprovalSubjectProcessControl, ApprovalSubjectHostExecution:
		return stage == ApprovalStagePreSpawn
	case ApprovalSubjectDirectIP, ApprovalSubjectLocalListen, ApprovalSubjectLoopbackConnect:
		return stage == ApprovalStagePreSpawn
	case ApprovalSubjectDestinationSet:
		return stage == ApprovalStagePreDial
	case ApprovalSubjectSecret:
		return stage == ApprovalStagePreSend
	case ApprovalSubjectWriteRootSet, ApprovalSubjectReadPathSet:
		return stage == ApprovalStagePreSpawn
	default:
		return false
	}
}

// OptionContinues checks whether the option releases the held subject.
func (p ApprovalPlan) OptionContinues(option ApprovalOption) bool {
	if option.Kind == ApprovalOptionRedacted || option.Kind == ApprovalOptionTracked {
		return p.Subject.Kind == ApprovalSubjectSecret
	}
	if p.Subject.Kind == ApprovalSubjectActionSet {
		return actionSetOptionContinues(p.Subject.Targets, option.Authority)
	}
	for _, delta := range option.Authority {
		switch p.Subject.Kind {
		case ApprovalSubjectAction, ApprovalSubjectDestinationSet, ApprovalSubjectSecret, ApprovalSubjectPackageSet, ApprovalSubjectProcessControl, ApprovalSubjectHostExecution:
			if delta.Kind == AuthorityCurrentAction || delta.Kind == AuthorityGenericGrant {
				return true
			}
		case ApprovalSubjectSocketSet:
			if delta.Kind == AuthoritySocketPermit || delta.Kind == AuthoritySocketChat {
				return true
			}
		case ApprovalSubjectDirectIP:
			if delta.Kind == AuthorityDirectIPPermit || delta.Kind == AuthorityDirectIPChat {
				return true
			}
		case ApprovalSubjectLocalListen:
			if delta.Kind == AuthorityLocalListenChat || delta.Kind == AuthorityCurrentAction {
				return true
			}
		case ApprovalSubjectLoopbackConnect:
			if delta.Kind == AuthorityLoopbackConnectChat || delta.Kind == AuthorityCurrentAction {
				return true
			}
		case ApprovalSubjectWriteRootSet:
			if delta.Kind == AuthorityWriteRootChat || delta.Kind == AuthorityGrantedPath {
				return true
			}
		case ApprovalSubjectReadPathSet:
			if delta.Kind == AuthorityReadPathChat {
				return true
			}
		case ApprovalSubjectActionSet:
		}
	}
	return false
}

func actionSetOptionContinues(targets []ApprovalTarget, authority []ApprovalAuthorityDelta) bool {
	required := map[string]bool{}
	for _, target := range targets {
		switch target.Kind {
		case "socket":
			required["socket"] = true
		case "direct_ip":
			required["direct_ip"] = true
		case "local_listen":
			required["local_listen"] = true
		case "loopback_connect":
			required["loopback_connect"] = true
		case "secret":
			required["secret"] = true
		case "write_root", "credential_file", "key_material":
			required["write_path"] = true
		case "read_path":
			required["read_path"] = true
		default:
			required["action"] = true
		}
	}
	covered := map[string]bool{}
	for _, delta := range authority {
		switch delta.Kind {
		case AuthorityCurrentAction, AuthorityGenericGrant:
			if delta.Kind == AuthorityGenericGrant && delta.Grant != nil && delta.Grant.Predicate.Category == ApprovalGrantCategorySecret {
				covered["secret"] = true
				continue
			}
			if delta.Kind == AuthorityCurrentAction {
				covered["secret"] = true
			}
			covered["action"] = true
			// Current-action covers both local-network axes on this spawn.
			covered["local_listen"] = true
			covered["loopback_connect"] = true
		case AuthoritySocketPermit, AuthoritySocketChat:
			covered["socket"] = true
		case AuthorityDirectIPPermit, AuthorityDirectIPChat:
			covered["direct_ip"] = true
		case AuthorityLocalListenChat:
			covered["local_listen"] = true
		case AuthorityLoopbackConnectChat:
			covered["loopback_connect"] = true
		case AuthorityWriteRootChat, AuthorityGrantedPath:
			covered["write_path"] = true
		case AuthorityReadPathChat:
			covered["read_path"] = true
		case AuthorityAskQuiet, AuthorityTrustDestination:
		}
	}
	if len(required) == 0 {
		return false
	}
	for axis := range required {
		if !covered[axis] {
			return false
		}
	}
	return true
}

func validApprovalRung(rung ApprovalOptionRung) bool { return rungRank(rung) < len(approvalRungOrder) }

// rungRank is the ordering key within a group. An unknown rung ranks last;
// validApprovalRung rejects it before ordering matters.
func rungRank(rung ApprovalOptionRung) int {
	for i, candidate := range approvalRungOrder {
		if candidate == rung {
			return i
		}
	}
	return len(approvalRungOrder)
}

// approvalGroupOrder is the render order of ladder group headings. The primary
// ladder is the empty heading and leads.
var approvalGroupOrder = []string{
	"", GroupAlsoAllow, GroupHostResources, GroupQuiet, GroupRedaction, GroupTrust,
}

func validApprovalGroup(group string) bool { return groupOrder(group) < len(approvalGroupOrder) }

func groupOrder(group string) int {
	for i, candidate := range approvalGroupOrder {
		if candidate == group {
			return i
		}
	}
	return len(approvalGroupOrder)
}

func sortApprovalOptions(in []ApprovalOption) []ApprovalOption {
	out := append([]ApprovalOption(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := strings.TrimSpace(out[i].Group), strings.TrimSpace(out[j].Group)
		if gi != gj {
			if groupOrder(gi) != groupOrder(gj) {
				return groupOrder(gi) < groupOrder(gj)
			}
			return gi < gj
		}
		return rungRank(out[i].Rung) < rungRank(out[j].Rung)
	})
	return out
}

// faceRungs is the recommended-option order: chat first, wider rungs last.
var faceRungs = []ApprovalOptionRung{ApprovalRungChat, ApprovalRungOnce, ApprovalRungDay, ApprovalRungProject, ApprovalRungDevice}

// refaceAfterFilter recomputes the face when narrowing the options removed it.
func (p *ApprovalPlan) refaceAfterFilter(face FaceContext) error {
	if _, ok := p.Option(p.RecommendedOptionID); ok {
		return nil
	}
	id, err := computeRecommendedOptionID(p.Subject.Kind, p.Reasons, p.Options, face)
	if err != nil {
		return err
	}
	p.RecommendedOptionID = id
	return nil
}

// computeRecommendedOptionID selects the recommended option.
func computeRecommendedOptionID(kind ApprovalSubjectKind, reasons []api.ApprovalGate, options []ApprovalOption, face FaceContext) (string, error) {
	if kind == ApprovalSubjectSecret {
		return secretFace(options, face.SecretManaged)
	}
	if len(reasons) == 1 && reasons[0] == api.GateAuthorityMisuse {
		for _, option := range options {
			if option.Kind == ApprovalOptionQuiet && option.Rung == ApprovalRungChat && !option.Disabled {
				return option.ID, nil
			}
		}
	}
	primary := primaryLadder(options)
	if len(primary) == 0 {
		return "", fmt.Errorf("approval plan has no face among %d options", len(options))
	}
	for _, want := range faceRungs {
		for _, option := range primary {
			if option.Rung == want && !option.Disabled {
				return option.ID, nil
			}
		}
	}
	return "", fmt.Errorf("approval plan has no selectable face among %d options", len(options))
}

// secretFace prefers tracking for raw values and release for managed values.
func secretFace(options []ApprovalOption, managed bool) (string, error) {
	if !managed {
		for _, option := range options {
			if option.Kind == ApprovalOptionTracked && !option.Disabled {
				return option.ID, nil
			}
		}
	}
	for _, option := range options {
		if option.Kind == ApprovalOptionLease && option.Scope == ApprovalGrantScopeChat &&
			strings.TrimSpace(option.Group) == "" && !option.Disabled {
			return option.ID, nil
		}
	}
	for _, option := range options {
		if option.Kind == ApprovalOptionCurrentAction && option.Rung == ApprovalRungUnchanged &&
			!option.Disabled {
			return option.ID, nil
		}
	}
	return "", fmt.Errorf("secret approval plan has no selectable send")
}

// primaryLadder is the unnamed ladder, falling back to every non-quiet option
// when a card carries only grouped rungs.
func primaryLadder(options []ApprovalOption) []ApprovalOption {
	var primary []ApprovalOption
	for _, option := range options {
		if option.Kind == ApprovalOptionQuiet {
			continue
		}
		if strings.TrimSpace(option.Group) == "" {
			primary = append(primary, option)
		}
	}
	if len(primary) > 0 {
		return primary
	}
	for _, option := range options {
		if option.Kind != ApprovalOptionQuiet {
			primary = append(primary, option)
		}
	}
	return primary
}

func (o ApprovalOption) validateGrantIdentities() error {
	// Disabled placeholders do not require a durable grant identity.
	if o.Disabled {
		return nil
	}
	for _, delta := range o.Authority {
		if delta.Grant == nil {
			continue
		}
		if err := delta.Grant.ValidateDurableIdentity(); err != nil {
			return err
		}
	}
	return nil
}

func (o ApprovalOption) validateKind() error {
	reusable := false
	for _, delta := range o.Authority {
		switch delta.Kind {
		case AuthorityGenericGrant, AuthoritySocketChat, AuthorityDirectIPChat, AuthorityWriteRootChat,
			AuthorityReadPathChat, AuthorityLocalListenChat, AuthorityLoopbackConnectChat, AuthorityGrantedPath,
			AuthorityTrustDestination:
			reusable = true
		case AuthorityCurrentAction, AuthoritySocketPermit, AuthorityDirectIPPermit, AuthorityAskQuiet:
		}
	}
	switch o.Kind {
	case ApprovalOptionCurrentAction:
		if o.Scope != "" || o.DecisionAction != ApprovalOptionApprove {
			return fmt.Errorf("current-action option has invalid scope or decision")
		}
		if o.Rung != ApprovalRungOnce && o.Rung != ApprovalRungUnchanged {
			return fmt.Errorf("current-action option has invalid rung %q", o.Rung)
		}
		if reusable {
			return fmt.Errorf("current-action option contains reusable authority")
		}
	case ApprovalOptionLease:
		if o.Scope != ApprovalGrantScopeChat && o.Scope != ApprovalGrantScopeProject && o.Scope != ApprovalGrantScopeDevice {
			return fmt.Errorf("lease option has invalid scope")
		}
		if o.Rung != ApprovalRungDay && o.Rung != ApprovalRungChat && o.Rung != ApprovalRungProject && o.Rung != ApprovalRungDevice {
			return fmt.Errorf("lease option has invalid rung %q", o.Rung)
		}
		if o.DecisionAction != ApprovalOptionApprove {
			return fmt.Errorf("lease option has invalid decision")
		}
		if !reusable {
			return fmt.Errorf("lease option has no reusable authority")
		}
	case ApprovalOptionRedacted:
		if o.Scope != "" || o.DecisionAction != ApprovalOptionRedact {
			return fmt.Errorf("redaction option has invalid scope or decision")
		}
		if o.Rung != ApprovalRungRedacted {
			return fmt.Errorf("redaction option has invalid rung %q", o.Rung)
		}
		if reusable {
			return fmt.Errorf("redaction option contains reusable authority")
		}
	case ApprovalOptionTracked:
		if o.Scope != "" || o.DecisionAction != ApprovalOptionTrack || o.Rung != ApprovalRungTracked {
			return fmt.Errorf("tracked option has invalid scope, decision, or rung")
		}
		if reusable {
			return fmt.Errorf("tracked option contains reusable authority")
		}
	case ApprovalOptionQuiet:
		if o.Scope != "" || o.DecisionAction != ApprovalOptionApprove {
			return fmt.Errorf("quiet option has invalid scope or decision")
		}
		// The quiet slot is one row: the rest of this chat.
		if o.Rung != ApprovalRungChat {
			return fmt.Errorf("quiet option has invalid rung %q", o.Rung)
		}
		hasQuiet := false
		for _, delta := range o.Authority {
			if delta.Kind == AuthorityAskQuiet {
				hasQuiet = true
			}
			// A quiet title states one day or the rest of this chat, so it may only
			// carry authority bounded the same way.
			if delta.Grant != nil && !delta.Grant.TimeBounded() {
				return fmt.Errorf("quiet option carries standing %s authority behind chat-bounded copy", delta.Grant.Scope)
			}
		}
		if !hasQuiet {
			return fmt.Errorf("quiet option has no ask_quiet authority")
		}
	default:
		return fmt.Errorf("unknown option kind %q", o.Kind)
	}
	return nil
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

func (d ApprovalAuthorityDelta) validate() error {
	if d.Grant != nil {
		if err := ValidateElevatedEffects(d.Grant.ElevatedEffects); err != nil {
			return err
		}
	}
	if d.AskQuiet != nil {
		if err := ValidateElevatedEffects(d.AskQuiet.ElevatedEffects); err != nil {
			return err
		}
	}
	if d.Kind != AuthorityLocalListenChat && len(d.ListenPorts) != 0 {
		return fmt.Errorf("authority %q carries unrelated listen ports", d.Kind)
	}
	if d.Kind != AuthorityLoopbackConnectChat && len(d.ConnectPorts) != 0 {
		return fmt.Errorf("authority %q carries unrelated connect ports", d.Kind)
	}
	switch d.Kind {
	case AuthorityCurrentAction:
		if d.hasGrant() || d.hasBoundaryFields() || d.TTLSeconds != 0 {
			return fmt.Errorf("current-action authority has unrelated fields")
		}
	case AuthorityGenericGrant:
		if !d.hasGrant() {
			return fmt.Errorf("generic grant is missing")
		}
		if d.ChatSessionID != "" || d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 || d.DirectIPLease != nil || len(d.WriteRoots) != 0 {
			return fmt.Errorf("generic grant has unrelated fields")
		}
	case AuthoritySocketPermit, AuthoritySocketChat:
		if strings.TrimSpace(d.ActionDigest) == "" || len(d.Sockets) == 0 {
			return fmt.Errorf("socket authority is incomplete")
		}
		if d.Kind == AuthoritySocketPermit {
			if d.hasGrant() || strings.TrimSpace(d.SessionID) == "" || strings.TrimSpace(d.ToolCallID) == "" || d.ChatSessionID != "" || d.DirectIPLease != nil || len(d.WriteRoots) != 0 || d.TTLSeconds != 0 {
				return fmt.Errorf("socket permit fields are invalid")
			}
		} else if !d.hasGrant() || strings.TrimSpace(d.ChatSession()) == "" || d.SessionID != "" || d.ToolCallID != "" || d.DirectIPLease != nil || len(d.WriteRoots) != 0 {
			return fmt.Errorf("socket chat fields are invalid")
		}
	case AuthorityDirectIPPermit, AuthorityDirectIPChat:
		if d.DirectIPLease == nil || !d.DirectIPLease.Complete() {
			return fmt.Errorf("direct-IP authority is incomplete")
		}
		if d.Kind == AuthorityDirectIPPermit {
			if d.hasGrant() || strings.TrimSpace(d.SessionID) == "" || strings.TrimSpace(d.ToolCallID) == "" || d.ActionDigest != d.DirectIPLease.ActionDigest || d.ChatSessionID != "" || len(d.Sockets) != 0 || len(d.WriteRoots) != 0 || d.TTLSeconds != 0 {
				return fmt.Errorf("direct-IP permit fields are invalid")
			}
		} else if !d.hasGrant() || strings.TrimSpace(d.ChatSession()) == "" || d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 || len(d.WriteRoots) != 0 {
			return fmt.Errorf("direct-IP chat fields are invalid")
		}
	case AuthorityWriteRootChat:
		if !d.hasGrant() ||
			strings.TrimSpace(d.ChatSession()) == "" || len(d.WriteRoots) == 0 {
			return fmt.Errorf("write-root authority is incomplete")
		}
		if d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 || d.DirectIPLease != nil || len(d.ReadPaths) != 0 {
			return fmt.Errorf("write-root authority has unrelated fields")
		}
	case AuthorityReadPathChat:
		if !d.hasGrant() ||
			strings.TrimSpace(d.ChatSession()) == "" || len(d.ReadPaths) == 0 {
			return fmt.Errorf("read-path authority is incomplete")
		}
		if d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 || d.DirectIPLease != nil || d.TTLSeconds != 0 || len(d.WriteRoots) != 0 {
			return fmt.Errorf("read-path authority has unrelated fields")
		}
	case AuthorityLocalListenChat:
		if !d.hasGrant() || strings.TrimSpace(d.ChatSession()) == "" {
			return fmt.Errorf("local-listen authority is incomplete")
		}
		if d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 ||
			len(d.WriteRoots) != 0 || d.DirectIPLease != nil {
			return fmt.Errorf("local-listen authority has unrelated fields")
		}
		for _, port := range d.ListenPorts {
			if port == 0 {
				return fmt.Errorf("local-listen authority names port 0")
			}
		}
	case AuthorityLoopbackConnectChat:
		if !d.hasGrant() || strings.TrimSpace(d.ChatSession()) == "" {
			return fmt.Errorf("loopback-connect authority is incomplete")
		}
		if d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" || len(d.Sockets) != 0 ||
			len(d.WriteRoots) != 0 || d.DirectIPLease != nil || len(d.ListenPorts) != 0 {
			return fmt.Errorf("loopback-connect authority has unrelated fields")
		}
		for _, port := range d.ConnectPorts {
			if port == 0 {
				return fmt.Errorf("loopback-connect authority names port 0")
			}
		}
	case AuthorityGrantedPath:
		if d.GrantedPath == nil || strings.TrimSpace(d.GrantedPath.Path) == "" ||
			!filepath.IsAbs(d.GrantedPath.Path) {
			return fmt.Errorf("granted-path authority needs an absolute path")
		}
		if !d.hasGrant() || strings.TrimSpace(d.ChatSession()) == "" {
			return fmt.Errorf("granted-path authority is incomplete")
		}
		if d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" ||
			len(d.Sockets) != 0 || d.DirectIPLease != nil || len(d.WriteRoots) != 0 {
			return fmt.Errorf("granted-path authority has unrelated fields")
		}
	case AuthorityTrustDestination:
		if d.TrustDestination == nil || strings.TrimSpace(d.TrustDestination.ProviderID) == "" ||
			strings.TrimSpace(d.TrustDestination.DestinationID) == "" || strings.TrimSpace(d.TrustDestination.Label) == "" {
			return fmt.Errorf("trust-destination authority is incomplete")
		}
		if !d.hasGrant() || d.hasBoundaryFields() || d.GrantedPath != nil || d.AskQuiet != nil || d.TTLSeconds != 0 {
			return fmt.Errorf("trust-destination authority has unrelated fields")
		}
	case AuthorityAskQuiet:
		if d.AskQuiet == nil || strings.TrimSpace(d.AskQuiet.ID) == "" ||
			strings.TrimSpace(d.AskQuiet.Key) == "" || strings.TrimSpace(d.AskQuiet.Label) == "" {
			return fmt.Errorf("ask-quiet authority is incomplete")
		}
		if strings.TrimSpace(d.ChatSessionID) == "" {
			return fmt.Errorf("ask-quiet authority needs a chat session")
		}
		if d.hasGrant() || d.SessionID != "" || d.ToolCallID != "" || d.ActionDigest != "" ||
			len(d.Sockets) != 0 || d.DirectIPLease != nil || len(d.WriteRoots) != 0 || d.GrantedPath != nil {
			return fmt.Errorf("ask-quiet authority has unrelated fields")
		}
		if d.TTLSeconds < 0 {
			return fmt.Errorf("ask-quiet authority has invalid ttl")
		}
	default:
		return fmt.Errorf("unknown authority delta %q", d.Kind)
	}
	return nil
}

func (d ApprovalAuthorityDelta) hasGrant() bool {
	return d.Grant != nil && strings.TrimSpace(d.Grant.ID) != ""
}

func (d ApprovalAuthorityDelta) hasBoundaryFields() bool {
	return d.ChatSessionID != "" || d.SessionID != "" || d.ToolCallID != "" ||
		d.ActionDigest != "" || len(d.Sockets) != 0 || d.DirectIPLease != nil || len(d.WriteRoots) != 0 ||
		len(d.ListenPorts) != 0 || len(d.ConnectPorts) != 0
}

// Option returns a plan option by its opaque id.
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
		Group: offer.Group, Title: offer.Title, Coverage: offer.Coverage,
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
