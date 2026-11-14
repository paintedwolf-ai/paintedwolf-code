package sourceapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/formatconfig"
	"github.com/lycaon/lycaon/internal/projectsource"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Workspace) HandleGetProjectSourceFormatSettings(w http.ResponseWriter, r *http.Request) {
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
	path, sources, err := projectsource.ResolveSourceFormatSettings(p, query.Get("path"), rootID)
	if err != nil {
		s.writeSourceReadError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, toSourceFormatSettingsDTO(path, rootID, sources))
}

func toSourceFormatSettingsDTO(path, rootID string, sources []formatconfig.Source) wire.SourceFormatSettings {
	out := wire.SourceFormatSettings{Path: path, RootID: rootID, Sources: []wire.FormatSettingsSource{}, ConfigNames: formatconfig.ConfigNames()}
	for _, source := range sources {
		s := source.Settings
		out.Sources = append(out.Sources, wire.FormatSettingsSource{
			Tool: source.Tool, Configs: source.Configs,
			Settings: wire.FormatSettings{
				LineWidth: s.LineWidth, IndentStyle: string(s.IndentStyle), IndentSize: s.IndentSize,
				IndentSizeTab: s.IndentSizeTab, TabWidth: s.TabWidth, EndOfLine: s.EndOfLine,
				TrimTrailingWhitespace: s.TrimTrailingWhitespace, InsertFinalNewline: s.InsertFinalNewline,
				Charset: s.Charset,
			},
		})
	}
	return out
}
