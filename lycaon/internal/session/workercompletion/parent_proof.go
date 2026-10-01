package workercompletion

// ParentProof is the parent-transcript slice of WorkerCompletionProof.
type ParentProof struct {
	ChangedPaths      []string `json:"changed_paths,omitempty"`
	WorkspaceDirty    bool     `json:"workspace_dirty,omitempty"`
	MutationTools     []string `json:"mutation_tools,omitempty"`
	SurveyTools       []string `json:"survey_tools,omitempty"`
	ReceiptCount      int      `json:"receipt_count,omitempty"`
	VisualArtifactIDs []string `json:"visual_artifact_ids,omitempty"`
}

// ParentProofFrom copies parent-visible fields from a host proof.
func ParentProofFrom(p WorkerCompletionProof) ParentProof {
	return ParentProof{
		ChangedPaths:      append([]string(nil), p.ChangedPaths...),
		WorkspaceDirty:    p.WorkspaceDirty,
		MutationTools:     append([]string(nil), p.MutationTools...),
		SurveyTools:       append([]string(nil), p.SurveyTools...),
		ReceiptCount:      p.ReceiptCount,
		VisualArtifactIDs: append([]string(nil), p.VisualArtifactIDs...),
	}
}

func (p ParentProof) Empty() bool {
	return len(p.ChangedPaths) == 0 && !p.WorkspaceDirty &&
		len(p.MutationTools) == 0 && len(p.SurveyTools) == 0 &&
		p.ReceiptCount == 0 && len(p.VisualArtifactIDs) == 0
}

func (p ParentProof) Proof() WorkerCompletionProof {
	return WorkerCompletionProof{
		ChangedPaths:      append([]string(nil), p.ChangedPaths...),
		WorkspaceDirty:    p.WorkspaceDirty,
		MutationTools:     append([]string(nil), p.MutationTools...),
		SurveyTools:       append([]string(nil), p.SurveyTools...),
		ReceiptCount:      p.ReceiptCount,
		VisualArtifactIDs: append([]string(nil), p.VisualArtifactIDs...),
	}
}
