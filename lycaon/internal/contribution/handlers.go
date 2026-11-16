package contribution

// HandlerInventories carries native UI handler coverage.
type HandlerInventories struct {
	Den []string
	// NonCommandCallbacks excludes callbacks that are not commands.
	NonCommandCallbacks []string
}
