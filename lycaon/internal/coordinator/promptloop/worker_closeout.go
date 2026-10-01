package promptloop

import (
	"github.com/lycaon/lycaon/internal/tools"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/pkg/api"
)

func workerProseOfferedTools(sess *api.Session) []string {
	if sess != nil && sess.IsWorkerChild() {
		return []string{workertools.CompleteLegTool}
	}
	return []string{}
}

func workerProseAllowsTool(sess *api.Session, name string) bool {
	return sess != nil && sess.IsWorkerChild() && name == workertools.CompleteLegTool
}

func workerProseToolMetas(all []tools.ToolMeta, sess *api.Session) []tools.ToolMeta {
	if sess == nil || !sess.IsWorkerChild() {
		return all
	}
	for _, meta := range all {
		if meta.Name == workertools.CompleteLegTool {
			return []tools.ToolMeta{meta}
		}
	}
	return []tools.ToolMeta{{Name: workertools.CompleteLegTool}}
}
