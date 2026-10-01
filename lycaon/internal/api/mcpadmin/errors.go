package mcpadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/mcp"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// mcpAdminCode names the catalog code of an MCP admin failure code; a code outside
// the MCP admin catalog has none.
func mcpAdminCode(code string) (wire.ApiErrorCode, bool) {
	switch code {
	case string(wire.ApiErrorCodeMcpIdRequired):
		return wire.ApiErrorCodeMcpIdRequired, true
	case string(wire.ApiErrorCodeMcpTransportRequired):
		return wire.ApiErrorCodeMcpTransportRequired, true
	case string(wire.ApiErrorCodeMcpTransportConflict):
		return wire.ApiErrorCodeMcpTransportConflict, true
	case string(wire.ApiErrorCodeMcpProviderNotFound):
		return wire.ApiErrorCodeMcpProviderNotFound, true
	case string(wire.ApiErrorCodeMcpUpdateEmpty):
		return wire.ApiErrorCodeMcpUpdateEmpty, true
	case string(wire.ApiErrorCodeMcpOauthUnavailable):
		return wire.ApiErrorCodeMcpOauthUnavailable, true
	case string(wire.ApiErrorCodeMcpOauthFailed):
		return wire.ApiErrorCodeMcpOauthFailed, true
	case string(wire.ApiErrorCodeMcpOauthNotSupported):
		return wire.ApiErrorCodeMcpOauthNotSupported, true
	case string(wire.ApiErrorCodeMcpOauthRegistrationRequired):
		return wire.ApiErrorCodeMcpOauthRegistrationRequired, true
	case string(wire.ApiErrorCodeMcpRecipeNotFound):
		return wire.ApiErrorCodeMcpRecipeNotFound, true
	case string(wire.ApiErrorCodeMcpPersistFailed):
		return wire.ApiErrorCodeMcpPersistFailed, true
	case string(wire.ApiErrorCodeMcpSyncFailed):
		return wire.ApiErrorCodeMcpSyncFailed, true
	case string(wire.ApiErrorCodeMcpProviderUnreachable):
		return wire.ApiErrorCodeMcpProviderUnreachable, true
	case string(wire.ApiErrorCodeMcpToolPinUnreadable):
		return wire.ApiErrorCodeMcpToolPinUnreadable, true
	case string(wire.ApiErrorCodeDuplicateId):
		return wire.ApiErrorCodeDuplicateId, true
	case string(wire.ApiErrorCodeInvalidEntry):
		return wire.ApiErrorCodeInvalidEntry, true
	case string(wire.ApiErrorCodeRemoteRequiresHttps):
		return wire.ApiErrorCodeRemoteRequiresHttps, true
	case string(wire.ApiErrorCodeInvalidUrl):
		return wire.ApiErrorCodeInvalidUrl, true
	case string(wire.ApiErrorCodeProjectStdioForbidden):
		return wire.ApiErrorCodeProjectStdioForbidden, true
	case string(wire.ApiErrorCodeProjectHeadersForbidden):
		return wire.ApiErrorCodeProjectHeadersForbidden, true
	case string(wire.ApiErrorCodeProjectRemoteForbidden):
		return wire.ApiErrorCodeProjectRemoteForbidden, true
	case string(wire.ApiErrorCodeProjectEnableForbidden):
		return wire.ApiErrorCodeProjectEnableForbidden, true
	case string(wire.ApiErrorCodeOverlayUnknownField):
		return wire.ApiErrorCodeOverlayUnknownField, true
	case string(wire.ApiErrorCodeUnreadableLayer):
		return wire.ApiErrorCodeUnreadableLayer, true
	}
	return "", false
}

func (s *Handler) writeMCPAdminError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	var ae *mcp.AdminError
	if !errors.As(err, &ae) {
		ae = mcp.AdminWrap(mcp.SyncFailureCode(err), err)
	}
	s.writeMCPAdminCode(w, ae)
}

func (s *Handler) writeMCPAdminCode(w http.ResponseWriter, ae *mcp.AdminError) {
	code, ok := mcpAdminCode(ae.Code)
	if !ok {
		s.responses.Logger.Error("MCP admin failure outside the catalog", "err", ae)
		s.responses.Fail(w, wire.ApiErrorCodeInternalError, "internal server error")
		return
	}
	ctx := map[string]any{}
	if id := strings.TrimSpace(ae.ProviderID); id != "" {
		ctx["provider_id"] = id
	}
	s.responses.FailDetails(w, code, ctx, "the MCP settings request was not applied")
}

func (s *Handler) decorateMCPProvider(row wire.McpProvider) wire.McpProvider {
	row.Notice = s.renderedNotice(row.LastError)
	return row
}

func (s *Handler) decorateMCPProviders(rows []wire.McpProvider) []wire.McpProvider {
	out := make([]wire.McpProvider, len(rows))
	for i, row := range rows {
		out[i] = s.decorateMCPProvider(row)
	}
	return out
}

func (s *Handler) decorateMCPCheckRow(row wire.McpCheckRow) wire.McpCheckRow {
	row.Notice = s.renderedNotice(row.Code)
	return row
}

func (s *Handler) decorateMCPCheckRows(rows []wire.McpCheckRow) []wire.McpCheckRow {
	out := make([]wire.McpCheckRow, len(rows))
	for i, row := range rows {
		out[i] = s.decorateMCPCheckRow(row)
	}
	return out
}

func (s *Handler) renderedNotice(code string) *wire.NoticeCopy {
	code = strings.TrimSpace(code)
	if code == "" || s == nil || s.responses.Notices == nil {
		return nil
	}
	if _, ok := mcpAdminCode(code); !ok {
		return nil
	}
	copy := s.responses.Notices.RenderWire(code, nil)
	if strings.TrimSpace(copy.Title) == "" || strings.TrimSpace(copy.Message) == "" {
		return nil
	}
	var actions []wire.NoticeAction
	for _, a := range copy.Actions {
		actions = append(actions, wire.NoticeAction(a))
	}
	return &wire.NoticeCopy{
		Title:           copy.Title,
		Message:         copy.Message,
		SuggestedAction: copy.SuggestedAction,
		Actions:         actions,
	}
}
