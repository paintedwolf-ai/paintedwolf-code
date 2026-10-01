// Package settings loads and persists user-editable approval rules and session
// limits, merged from bundled defaults, user settings, and project overlays under
// the same YAML filename at each layer. Project writes require an open project;
// settings files use mode 0600.
package settings
