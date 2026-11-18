package sourcefeed

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
