package sourcefeed

// EmitDoor identifies a host mutation boundary.
type EmitDoor string

const (
	DoorAgentMutation   EmitDoor = "agent.mutation"
	DoorEditorSave      EmitDoor = "editor.save"
	DoorWorkerPromotion EmitDoor = "worker.promotion"
	DoorUserPut         EmitDoor = "user.put"
	DoorUserPost        EmitDoor = "user.post"
	DoorRename          EmitDoor = "lifecycle.rename"
	DoorDelete          EmitDoor = "lifecycle.delete"
	DoorCopy            EmitDoor = "lifecycle.copy"
	DoorReplaceApply    EmitDoor = "search.replace_apply"
)

// AllEmitDoors is the closed set of host write doors that must emit exactly once.
func AllEmitDoors() []EmitDoor {
	return []EmitDoor{
		DoorAgentMutation,
		DoorEditorSave,
		DoorWorkerPromotion,
		DoorUserPut,
		DoorUserPost,
		DoorRename,
		DoorDelete,
		DoorCopy,
		DoorReplaceApply,
	}
}
