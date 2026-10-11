package definition

// DepthFanOutCount maps depth to parallel fan-out width.
func DepthFanOutCount(level DepthLevel) int {
	switch level {
	case DepthNone:
		return 0
	case DepthThorough:
		return 3
	default:
		return 1
	}
}
