package promptinput

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// HostSignal is the machine identity persisted on a host turn receipt.
type HostSignal struct {
	Kind api.MessageKind `json:"kind"`
	ID   string          `json:"id"`
}

// Input is one user-facing or host-authored turn.
type Input struct {
	SourceContext *api.SourceContext `json:"source_context,omitempty"`
	SubmissionID  string             `json:"submission_id,omitempty"`
	// SubmissionIDs binds coalesced receipts to one execution turn.
	SubmissionIDs []string `json:"-"`
	// AuthorPersonID is the sender recorded on the admission receipt.
	AuthorPersonID string `json:"-"`
	// WorkerJobID binds retry attempts to one semantic worker turn.
	WorkerJobID  string                   `json:"worker_job_id,omitempty"`
	Text         string                   `json:"text"`
	ArtifactIDs  []string                 `json:"artifact_ids,omitempty"`
	ContentParts []api.MessageContentPart `json:"content_parts,omitempty"`
	// ToolProfile overrides the session tool profile for this turn.
	ToolProfile string `json:"tool_profile,omitempty"`
	// WritePinRootID and WritePinGlobs constrain writes under one root.
	WritePinRootID string      `json:"write_pin_root_id,omitempty"`
	WritePinGlobs  []string    `json:"write_pin_globs,omitempty"`
	HostSignal     *HostSignal `json:"host_signal,omitempty"`
	// ProseFinish closes the whole run.
	ProseFinish bool `json:"prose_finish,omitempty"`
	// Continuation delivers explicit user direction inside the open visible turn.
	Continuation bool                `json:"continuation,omitempty"`
	Recovery     *api.PromptRecovery `json:"recovery,omitempty"`
}

func (in Input) HostSignalID() string {
	if in.HostSignal == nil {
		return ""
	}
	return strings.TrimSpace(in.HostSignal.ID)
}

func (in Input) UserInstruction() string {
	if len(in.ContentParts) == 0 {
		return strings.TrimSpace(in.Text)
	}
	return strings.TrimSpace(api.MessageUserInstructionContent(api.Message{
		Role: api.MessageRoleUser, Content: in.Text,
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
		ContentParts: in.ContentParts,
	}))
}

func Coalesce(inputs []Input) Input {
	texts := make([]string, 0, len(inputs))
	submissionIDs := make([]string, 0, len(inputs))
	continuation := false
	author := ""
	for _, in := range inputs {
		if author == "" {
			author = in.AuthorPersonID
		}
		if t := strings.TrimSpace(in.Text); t != "" {
			texts = append(texts, t)
		}
		if id := strings.TrimSpace(in.SubmissionID); id != "" {
			submissionIDs = append(submissionIDs, id)
		}
		continuation = continuation || in.Continuation
	}
	return Input{
		Text: strings.Join(texts, "\n\n"), SubmissionIDs: submissionIDs,
		Continuation: continuation, AuthorPersonID: author,
	}
}
func (in Input) RidesQueue() bool {
	return strings.TrimSpace(in.Text) != "" &&
		len(in.ArtifactIDs) == 0 &&
		len(in.ContentParts) == 0 &&
		(in.SourceContext == nil || len(in.SourceContext.Locations) == 0) &&
		strings.TrimSpace(in.ToolProfile) == "" &&
		strings.TrimSpace(in.WritePinRootID) == "" &&
		len(in.WritePinGlobs) == 0 &&
		in.HostSignal == nil &&
		!in.ProseFinish && in.Recovery == nil
}

// FromSubmission rejects fields the text draft cannot preserve.
func FromSubmission(row *store.PromptSubmission) (Input, error) {
	var in Input
	if err := json.Unmarshal([]byte(row.InputJSON), &in); err != nil {
		return Input{}, fmt.Errorf("stored prompt input is invalid: %w", err)
	}
	if !in.RidesQueue() {
		return Input{}, fmt.Errorf("stored prompt input cannot ride the next-turn draft")
	}
	in.AuthorPersonID = row.SubmittedBy
	return in, nil
}
