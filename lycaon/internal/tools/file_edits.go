package tools

import "github.com/lycaon/lycaon/pkg/api"

// RecordFileEdit records a landed text change on o.
func (o *ToolInvocationOut) RecordFileEdit(edit api.FileEditSnapshot) {
	var held bool
	if o.FileEdits, held = api.RecordFileEdit(o.FileEdits, edit); !held {
		o.FileEditsOmitted++
	}
}
