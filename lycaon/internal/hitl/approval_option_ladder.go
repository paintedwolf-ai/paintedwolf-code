package hitl

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

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
