package evidence

import "strings"

type identityNormalizer struct{}

func (identityNormalizer) Normalize(token string) string {
	return strings.TrimSpace(token)
}

type FileRegionNormalizer struct{}

func (FileRegionNormalizer) Normalize(token string) string {
	token = strings.TrimSpace(token)
	token = strings.ReplaceAll(token, `\`, "/")
	token = strings.TrimPrefix(token, "./")
	if len(token) >= 2 && token[1] == ':' {
		token = strings.ToLower(token[:1]) + token[1:]
	}
	return strings.TrimRight(token, "/")
}
