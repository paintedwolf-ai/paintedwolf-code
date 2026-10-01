package api

// --- Blueprint ---

type BlueprintStatus string

const (
	BlueprintStatusDraft        BlueprintStatus = "draft"
	BlueprintStatusApproved     BlueprintStatus = "approved"
	BlueprintStatusImplementing BlueprintStatus = "implementing"
	BlueprintStatusDone         BlueprintStatus = "done"
)
