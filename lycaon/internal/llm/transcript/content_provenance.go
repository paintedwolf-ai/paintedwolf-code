// Package transcript projects typed message authority and host feedback into provider-ready content.
package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/pkg/api"
)

// ContentAuthorityNotice loads the instruction/data notice, panicking if unavailable.
func ContentAuthorityNotice() string {
	loadContentAuthority()
	return contentAuthorityNotice
}

// CheckpointRejectionNotice frames a human denial even without free-text guidance.
func CheckpointRejectionNotice() string {
	loadContentAuthority()
	return checkpointRejectionNotice
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
			CheckpointGuidance  string `yaml:"checkpoint_guidance"`
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
		if checkpointRejectionNotice = strings.TrimSpace(doc.CheckpointRejection); checkpointRejectionNotice == "" {
			contentAuthorityErr = fmt.Errorf("content-authority.yaml declares no checkpoint_rejection")
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
	checkpointGuidanceNotice  string
	checkpointRejectionNotice string
	contentAuthorityErr       error
)

const (
	contentAuthorityNoticeSource = "content_authority_notice"
	checkpointDenialSource       = "checkpoint_denial"
	checkpointGuidanceSource     = "checkpoint_guidance"
	checkpointDecisionSource     = "checkpoint_decision"
)

// Project returns detached, model-ready messages with one authority notice.
// ModelProjected prevents repeated projection from duplicating data markers.
func Project(messages []api.Message) []api.Message {
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
		parts := msg.ContentParts
		if len(parts) == 0 && msg.Authority == api.ContentAuthorityNone {
			parts = api.MessageTextParts(msg)
		}
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
				out[i] = mergeContentAuthorityNotice(out[i])
				return out
			}
		}
		out = append([]api.Message{contentAuthorityNoticeMessage()}, out...)
	}
	return out
}

func contentAuthorityNoticeMessage() api.Message {
	return api.Message{
		Role:      api.MessageRoleSystem,
		Content:   ContentAuthorityNotice(),
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		ModelProjected: true,
	}
}

func HasAuthorityNotice(msg api.Message) bool {
	if msg.Role == api.MessageRoleSystem && msg.Origin == api.MessageOriginHost &&
		msg.Authority == api.ContentAuthoritySystem && msg.Content == ContentAuthorityNotice() {
		return true
	}
	for _, part := range msg.ContentParts {
		if part.Origin == api.MessageOriginHost && part.Authority == api.ContentAuthoritySystem &&
			part.Source == contentAuthorityNoticeSource && part.Content == ContentAuthorityNotice() {
			return true
		}
	}
	return false
}

func mergeContentAuthorityNotice(msg api.Message) api.Message {
	parts := []api.MessageContentPart{{
		Content: ContentAuthorityNotice(), Origin: api.MessageOriginHost,
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
		return checkpointReceiptParts(*decision)
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
		label := string(part.Origin)
		if source := strings.TrimSpace(part.Source); source != "" {
			label += ":" + source
		}
		out = append(out, fmt.Sprintf("⟦D:%s⟧\n%s", label, dataMarkLines(content)))
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

func dataMarkLines(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = "⟦D⟧" + lines[i]
	}
	return strings.Join(lines, "\n")
}

// MarkData marks non-authoritative utility-prompt content.
func MarkData(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	return dataMarkLines(content)
}
