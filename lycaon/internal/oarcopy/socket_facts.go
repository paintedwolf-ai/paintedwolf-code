package oarcopy

import (
	"github.com/lycaon/lycaon/internal/confine"
	"strings"
)

func publishSocketReferences(facts, data map[string]any) {
	paths, _ := data["refused_socket_paths"].([]string)
	if values, ok := data["refused_socket_paths"].([]any); ok {
		for _, value := range values {
			if path, ok := value.(string); ok {
				paths = append(paths, path)
			}
		}
	}
	if code, _ := data["rejection_code"].(string); code == "SANDBOX_SOCKET_PERMIT_REQUIRED" {
		if path, ok := data["path"].(string); ok {
			paths = append(append([]string(nil), paths...), path)
		}
	}
	var refs, urls []string
	for _, path := range paths {
		ref, url := confine.SocketEndpointReferences(path)
		if ref != "" {
			refs = append(refs, ref)
			urls = append(urls, url)
		}
	}
	facts["paintedwolf.socket_path_references"] = strings.Join(refs, "`, `")
	facts["paintedwolf.socket_url_references"] = strings.Join(urls, "`, `")
}
