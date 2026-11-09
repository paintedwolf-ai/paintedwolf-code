package confine

import (
	"net/url"
	"strings"
)

// SocketEndpointReferences formats declaration references without granting access.
func SocketEndpointReferences(path string) (string, string) {
	if socketPathSyntaxReason(path) != "" {
		return "", ""
	}
	encoded := strings.ReplaceAll(url.QueryEscape(path), "+", "%20")
	encoded = strings.ReplaceAll(encoded, "%2F", "/")
	return socketReferencePrefix + encoded + "}}", socketReferencePrefix + "unix-url:" + encoded + "}}"
}
