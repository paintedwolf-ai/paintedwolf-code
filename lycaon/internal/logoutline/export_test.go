package logoutline

// MaskMessageForTest exposes maskMessage for tests in logoutline_test.
func MaskMessageForTest(msg string) string {
	return maskMessage(msg)
}
