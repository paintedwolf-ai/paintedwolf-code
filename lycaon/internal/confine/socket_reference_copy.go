package confine

import "net/url"

// SocketEndpointReferences formats declaration references without granting access.
func SocketEndpointReferences(path string) (string, string) {
	if socketPathSyntaxReason(path) != "" {
		return "", ""
	}
	encoded := (&url.URL{Path: path}).EscapedPath()
	return socketReferencePrefix + encoded + "}}", socketReferencePrefix + "unix-url:" + encoded + "}}"
}
