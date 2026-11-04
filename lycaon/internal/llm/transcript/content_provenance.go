// Package transcript projects typed message authority and host feedback into provider-ready content.
package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

// ContentAudience selects what a notice says about text accompanying tool calls,
// which differs by who wrote it and whether the surface publishes it.
type ContentAudience string

const (
	// AudienceUtility is a host utility prompt with no tool steps, such as a summary.
	AudienceUtility ContentAudience = "utility"
	// AudiencePublisher writes on a surface that publishes valid tool-step text.
	AudiencePublisher ContentAudience = "publisher"
	// AudiencePrivate writes tool-step text only it reads: a worker, or a root
	// session on a surface that does not publish.
	AudiencePrivate ContentAudience = "private"
)

// ContentAuthorityNotice loads the instruction/data notice for an audience,
// panicking if unavailable.
func ContentAuthorityNotice(audience ContentAudience) string {
	loadContentAuthority()
	switch audience {
	case AudiencePublisher:
		return contentAuthorityNotice + " " + publisherToolTextNotice
	case AudiencePrivate:
		return contentAuthorityNotice + " " + privateToolTextNotice
	case AudienceUtility:
		return contentAuthorityNotice
	default:
		panic(fmt.Errorf("unknown content audience %q", audience))
	}
}

func isContentAuthorityNotice(content string) bool {
	for _, audience := range []ContentAudience{AudienceUtility, AudiencePublisher, AudiencePrivate} {
		if content == ContentAuthorityNotice(audience) {
			return true
		}
	}
	return false
}

// CheckpointRejectionNotice frames a human denial even without free-text guidance.
func CheckpointRejectionNotice() string {
	loadContentAuthority()
	return checkpointRejectionNotice
}

// CheckpointApprovalNotice scopes continuation to the original task and actual result.
func CheckpointApprovalNotice() string {
	loadContentAuthority()
	return checkpointApprovalNotice
}

// CheckpointGuidanceNotice loads the denial-guidance frame, panicking if unavailable.
func CheckpointGuidanceNotice() string {
	loadContentAuthority()
	return checkpointGuidanceNotice
}

func loadContentAuthority() {
	contentAuthorityOnce.Do(func() {
		data, err := config.Read(config.ContentAuthority)
		if err != nil {
			contentAuthorityErr = fmt.Errorf("read content-authority.yaml: %w", err)
			return
		}
		var doc struct {
			Notice              string `yaml:"notice"`
			PublisherToolText   string `yaml:"publisher_tool_text"`
			PrivateToolText     string `yaml:"private_tool_text"`
			CheckpointGuidance  string `yaml:"checkpoint_guidance"`
			CheckpointApproval  string `yaml:"checkpoint_approval"`
			CheckpointRejection string `yaml:"checkpoint_rejection"`
		}
		if err := config.DecodeYAML(data, &doc); err != nil {
			contentAuthorityErr = fmt.Errorf("parse content-authority.yaml: %w", err)
			return
		}
		if contentAuthorityNotice = strings.TrimSpace(doc.Notice); contentAuthorityNotice == "" {
			contentAuthorityErr = fmt.Errorf("content-authority.yaml declares no notice")
			return
		}
		if publisherToolTextNotice = strings.TrimSpace(doc.PublisherToolText); publisherToolTextNotice == "" {
			contentAuthorityErr = fmt.Errorf("content-authority.yaml declares no publisher_tool_text")
			return
		}
		if privateToolTextNotice = strings.TrimSpace(doc.PrivateToolText); privateToolTextNotice == "" {
			contentAuthorityErr = fmt.Errorf("content-authority.yaml declares no private_tool_text")
			return
		}
		if checkpointRejectionNotice = strings.TrimSpace(doc.CheckpointRejection); checkpointRejectionNotice == "" {
			contentAuthorityErr = fmt.Errorf("content-authority.yaml declares no checkpoint_rejection")
			return
		}
		if checkpointApprovalNotice = strings.TrimSpace(doc.CheckpointApproval); checkpointApprovalNotice == "" {
			contentAuthorityErr = fmt.Errorf("content-authority.yaml declares no checkpoint_approval")
			return
		}
		if checkpointGuidanceNotice = strings.TrimSpace(doc.CheckpointGuidance); checkpointGuidanceNotice == "" {
			contentAuthorityErr = fmt.Errorf("content-authority.yaml declares no checkpoint_guidance")
		}
	})
	if contentAuthorityErr != nil {
		panic(contentAuthorityErr)
	}
}

var (
	contentAuthorityOnce      sync.Once
	contentAuthorityNotice    string
	publisherToolTextNotice   string
	privateToolTextNotice     string
	checkpointGuidanceNotice  string
	checkpointApprovalNotice  string
	checkpointRejectionNotice string
	contentAuthorityErr       error
)

const (
	contentAuthorityNoticeSource = "content_authority_notice"
	checkpointDenialSource       = "checkpoint_denial"
	checkpointGuidanceSource     = "checkpoint_guidance"
	checkpointDecisionSource     = "checkpoint_decision"
	checkpointApprovalSource     = "checkpoint_approval"
)

// Project returns detached, model-ready messages with one utility authority
// notice. A request that already carries a notice keeps it.
func Project(messages []api.Message) []api.Message {
	return ProjectFor(AudienceUtility, messages)
}

// ProjectFor projects for an audience. ModelProjected prevents repeated
// projection from duplicating data headers.
func ProjectFor(audience ContentAudience, messages []api.Message) []api.Message {
	out := make([]api.Message, 0, len(messages)+1)
	hasNotice := false
	for _, raw := range messages {
		msg := api.NormalizeMessageProvenance(raw)
		if HasAuthorityNotice(msg) {
			hasNotice = true
		}
		if msg.ModelProjected {
			out = append(out, msg)
			continue
		}
		if msg.Origin == api.MessageOriginHost && msg.Authority == api.ContentAuthoritySystem &&
			msg.Role == api.MessageRoleUser && msg.Visibility == api.MessageVisibilityInternal {
			msg.Role = api.MessageRoleSystem
		}
		msg = stripAssistantTransportMarkers(msg)
		parts := msg.ContentParts
		if len(parts) == 0 && msg.Authority == api.ContentAuthorityNone {
			parts = api.MessageTextParts(msg)
		}
		parts = projectSourceText(msg, parts)
		if decision := checkpointDecisionParts(msg); len(decision) > 0 {
			parts = append(append([]api.MessageContentPart(nil), parts...), decision...)
		}
		if feedback := toolFeedbackParts(msg); len(feedback) > 0 {
			parts = append(append([]api.MessageContentPart(nil), parts...), feedback...)
		}
		if len(parts) > 0 {
			msg.Content = ProjectContentParts(msg.Role, parts)
		}
		msg.ModelProjected = true
		out = append(out, msg)
	}
	if !hasNotice {
		for i := range out {
			if out[i].Role == api.MessageRoleSystem && out[i].Origin == api.MessageOriginHost &&
				out[i].Authority == api.ContentAuthoritySystem {
				out[i] = mergeContentAuthorityNotice(audience, out[i])
				return out
			}
		}
		out = append([]api.Message{contentAuthorityNoticeMessage(audience)}, out...)
	}
	return out
}

func contentAuthorityNoticeMessage(audience ContentAudience) api.Message {
	return api.Message{
		Role:      api.MessageRoleSystem,
		Content:   ContentAuthorityNotice(audience),
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		ModelProjected: true,
	}
}

func HasAuthorityNotice(msg api.Message) bool {
	if msg.Role == api.MessageRoleSystem && msg.Origin == api.MessageOriginHost &&
		msg.Authority == api.ContentAuthoritySystem && isContentAuthorityNotice(msg.Content) {
		return true
	}
	for _, part := range msg.ContentParts {
		if part.Origin == api.MessageOriginHost && part.Authority == api.ContentAuthoritySystem &&
			part.Source == contentAuthorityNoticeSource && isContentAuthorityNotice(part.Content) {
			return true
		}
	}
	return false
}

func mergeContentAuthorityNotice(audience ContentAudience, msg api.Message) api.Message {
	parts := []api.MessageContentPart{{
		Content: ContentAuthorityNotice(audience), Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, Source: contentAuthorityNoticeSource,
	}}
	parts = append(parts, api.MessageTextParts(msg)...)
	msg.ContentParts = parts
	msg.Content = ProjectContentParts(msg.Role, parts)
	msg.ModelProjected = true
	return msg
}

// Only explicit human guidance from a checkpoint becomes user instruction.
func checkpointDecisionParts(msg api.Message) []api.MessageContentPart {
	if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
		return nil
	}
	decision := msg.ToolResult.CheckpointDecision
	if decision != nil && decision.Status == api.CheckpointStatusApproved {
		parts := checkpointReceiptParts(*decision)
		if decision.Kind == api.CheckpointKindToolApproval {
			parts = append(parts, api.MessageContentPart{
				Content: CheckpointApprovalNotice(), Origin: api.MessageOriginHost,
				Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
				Source: checkpointApprovalSource,
			})
		}
		return parts
	}
	if decision == nil || decision.Status != api.CheckpointStatusRejected {
		return nil
	}
	parts := checkpointReceiptParts(*decision)
	parts = append(parts, api.MessageContentPart{
		Content: CheckpointRejectionNotice(), Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		Source: checkpointDenialSource,
	})
	direction := strings.TrimSpace(decision.Guidance)
	if direction == "" {
		return parts
	}
	return append(parts, api.MessageContentPart{
		Content: CheckpointGuidanceNotice(), Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		Source: checkpointDenialSource,
	}, api.MessageContentPart{
		Content: direction, Origin: api.MessageOriginUser,
		Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
		Source: checkpointGuidanceSource,
	})
}

func checkpointReceiptParts(decision api.CheckpointDecisionMeta) []api.MessageContentPart {
	decision.Guidance = ""
	body, err := json.Marshal(struct {
		Decision api.CheckpointDecisionMeta `json:"human_checkpoint"`
	}{Decision: decision})
	if err != nil {
		return nil
	}
	return []api.MessageContentPart{{
		Content: string(body), Origin: api.MessageOriginHost,
		Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
		Source: checkpointDecisionSource,
	}}
}

func ProjectContentParts(role api.MessageRole, parts []api.MessageContentPart) string {
	var out []string
	for _, raw := range parts {
		part := raw
		content := strings.TrimSpace(part.Content)
		if content == "" {
			continue
		}
		if contentPartIsInstruction(part.Authority) {
			out = append(out, content)
			continue
		}
		if contentPartUsesNativeRole(role, part) {
			out = append(out, content)
			continue
		}
		out = append(out, dataHeader(part.Origin, part.Source)+"\n"+content)
	}
	return strings.Join(out, "\n\n")
}

// Assistant roles structurally identify model output without a transport label.
func contentPartUsesNativeRole(role api.MessageRole, part api.MessageContentPart) bool {
	return role == api.MessageRoleAssistant &&
		part.Origin == api.MessageOriginModel && part.Authority == api.ContentAuthorityNone
}

func contentPartIsInstruction(authority api.ContentAuthority) bool {
	switch authority {
	case api.ContentAuthoritySystem, api.ContentAuthorityUser, api.ContentAuthorityDeveloper:
		return true
	default:
		return false
	}
}

// dataHeader is the one host-authored line above an authority-none part. It names
// where the part came from; authority itself comes from structured provenance.
func dataHeader(origin api.MessageOrigin, source string) string {
	// A source can be a file name, which must not break the single line.
	source = strings.Join(strings.Fields(source), " ")
	if source == "" {
		return fmt.Sprintf("Data from %s:", origin)
	}
	return fmt.Sprintf("Data from %s (%s):", origin, source)
}

// LabelToolMetadata heads externally authored tool metadata with its data header.
func LabelToolMetadata(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	return dataHeader(api.MessageOriginTool, "tool_metadata") + "\n" + content
}

// stripAssistantTransportMarkers removes markers the model parroted from older
// transcripts. Notes were stripped when published.
func stripAssistantTransportMarkers(msg api.Message) api.Message {
	if msg.Role != api.MessageRoleAssistant || api.IsAgentNoteMessage(msg) {
		return msg
	}
	msg.Content = hostmarker.StripParrotedTransportMarkers(msg.Content)
	if len(msg.ContentParts) == 0 {
		return msg
	}
	parts := make([]api.MessageContentPart, 0, len(msg.ContentParts))
	for _, part := range msg.ContentParts {
		if part.Origin == api.MessageOriginModel {
			part.Content = hostmarker.StripParrotedTransportMarkers(part.Content)
		}
		parts = append(parts, part)
	}
	msg.ContentParts = parts
	return msg
}
