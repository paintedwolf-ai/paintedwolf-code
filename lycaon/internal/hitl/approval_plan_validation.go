package hitl

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
	"slices"
	"strings"
)

// Validate enforces the invariants that make the plan safe to render and apply.
func (p ApprovalPlan) Validate() error {
	if err := p.validateDirectoryScopes(); err != nil {
		return err
	}
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
		groupKey := strings.TrimSpace(option.Group) + "\x00" + string(option.Rung) + "\x00" + option.DirectoryScope
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
