package webresearch

import (
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

func hostDeniedReject(host string) *tools.ToolReject {
	return &tools.ToolReject{
		Code: "WEB_SEARCH_HOST_DENIED",
		Data: map[string]any{"host": strings.TrimSpace(host)},
	}
}
