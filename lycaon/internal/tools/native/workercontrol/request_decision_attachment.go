package workercontrol

import (
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/tools"
)

type decisionAttachment struct {
	Single  string
	Compare []string
}

func decisionAttachmentShape(reason string) error {
	return &tools.ToolReject{Code: "REQUEST_DECISION_ARTIFACT_SHAPE", Data: map[string]any{"reason": reason}}
}

func parseDecisionAttachment(args map[string]any) (decisionAttachment, error) {
	single, hasSingle := args["artifact_id"]
	multiple, hasMultiple := args["artifact_ids"]
	if hasSingle && hasMultiple {
		return decisionAttachment{}, decisionAttachmentShape("artifact_id and artifact_ids are mutually exclusive")
	}
	if hasSingle {
		id, err := decisionArtifactID(single)
		return decisionAttachment{Single: id}, err
	}
	if !hasMultiple {
		return decisionAttachment{}, nil
	}
	var raw []any
	switch values := multiple.(type) {
	case []any:
		raw = values
	case []string:
		raw = make([]any, len(values))
		for i, v := range values {
			raw[i] = v
		}
	default:
		return decisionAttachment{}, decisionAttachmentShape("artifact_ids must be an array of UUID strings")
	}
	if len(raw) < 2 || len(raw) > 4 {
		return decisionAttachment{}, &tools.ToolReject{Code: "REQUEST_DECISION_ARTIFACT_ARITY", Data: map[string]any{"count": len(raw)}}
	}
	out := decisionAttachment{Compare: make([]string, 0, len(raw))}
	seen := map[string]bool{}
	for _, value := range raw {
		id, err := decisionArtifactID(value)
		if err != nil {
			return decisionAttachment{}, err
		}
		if seen[id] {
			return decisionAttachment{}, decisionAttachmentShape("artifact_ids must name distinct artifacts")
		}
		seen[id] = true
		out.Compare = append(out.Compare, id)
	}
	return out, nil
}

func decisionArtifactID(raw any) (string, error) {
	value, ok := raw.(string)
	if !ok {
		return "", decisionAttachmentShape("artifact IDs must be UUID strings")
	}
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", decisionAttachmentShape("artifact IDs must be nonempty valid UUID strings")
	}
	return id.String(), nil
}
