//go:build linux

package linuxsandbox

// WithSocketConnectPauseForTest synchronizes the real supervisor without adding
// any serialized plan field or production configuration entry point.
func WithSocketConnectPauseForTest(network Network, pause func()) Network {
	network.beforeSocketConnect = pause
	return network
}
