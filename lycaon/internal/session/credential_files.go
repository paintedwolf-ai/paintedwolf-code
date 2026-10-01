package session

import "github.com/lycaon/lycaon/internal/tools"

// SetCredentialFiles installs the receiver of file tools' credential-file facts.
func (m *Manager) SetCredentialFiles(files tools.CredentialFiles) {
	if m != nil {
		m.credentialFiles = files
	}
}
