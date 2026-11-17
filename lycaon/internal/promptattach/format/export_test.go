package format

// IsVideoMIME reports whether mime is an accepted video container.
func IsVideoMIME(mime string) bool {
	_, ok := videoMIME[normalizeMIME(mime)]
	return ok
}
