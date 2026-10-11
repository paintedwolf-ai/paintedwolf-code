package toolhost

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// MutationServices owns reviewed file mutation tools.
type MutationServices struct {
	writeTool          *native.WriteTool
	editTool           *native.EditTool
	replaceLinesTool   *native.ReplaceLinesTool
	codeRewriteTool    *native.CodeRewriteTool
	restoreVersionTool *native.RestoreVersionTool
	jqEditTool         *native.JqEditTool
}

func (r *MutationServices) SetContentApply(contentApply native.ContentApplyGate) {
	if r == nil {
		return
	}
	if r.writeTool != nil {
		r.writeTool.ContentApply = contentApply
	}
	if r.editTool != nil {
		r.editTool.ContentApply = contentApply
	}
	if r.replaceLinesTool != nil {
		r.replaceLinesTool.ContentApply = contentApply
	}
	if r.codeRewriteTool != nil {
		r.codeRewriteTool.ContentApply = contentApply
	}
	if r.restoreVersionTool != nil {
		r.restoreVersionTool.ContentApply = contentApply
	}
	if r.jqEditTool != nil {
		r.jqEditTool.ContentApply = contentApply
	}
}

func (r *MutationServices) SetBlueprintWriteObserver(o native.BlueprintWriteObserver) func(context.Context) error {
	if r == nil {
		return func(context.Context) error { return nil }
	}
	return native.SetBlueprintWriteObserver(o)
}
