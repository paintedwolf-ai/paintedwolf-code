package webresearch

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
)

func hostDeniedReject(host string) *toolrejection.ToolReject {
	return &toolrejection.ToolReject{
		Code: "WEB_SEARCH_HOST_DENIED",
		Data: map[string]any{"host": strings.TrimSpace(host)},
	}
}
