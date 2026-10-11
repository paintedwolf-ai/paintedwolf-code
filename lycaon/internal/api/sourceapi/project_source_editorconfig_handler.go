package sourceapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/editorconfig"
	"github.com/lycaon/lycaon/internal/projectsource"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Workspace) HandleGetProjectSourceEditorConfig(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	query := r.URL.Query()
	rootID := strings.TrimSpace(query.Get("root_id"))
	if rootID == "" {
		s.responses.InvalidQuery(w, errors.New("root_id is required"))
		return
	}
	path, props, err := projectsource.ResolveSourceEditorConfig(p, query.Get("path"), rootID)
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, toSourceEditorConfigDTO(path, rootID, props))
}

func toSourceEditorConfigDTO(path, rootID string, props editorconfig.Properties) wire.SourceEditorConfig {
	return wire.SourceEditorConfig{
		Path: path, RootID: rootID,
		IndentStyle: string(props.IndentStyle), IndentSize: props.IndentSize, IndentSizeTab: props.IndentSizeTab,
		TabWidth: props.TabWidth, EndOfLine: string(props.EndOfLine),
		TrimTrailingWhitespace: props.TrimTrailingWhitespace, InsertFinalNewline: props.InsertFinalNewline,
		Charset: props.Charset,
	}
}
