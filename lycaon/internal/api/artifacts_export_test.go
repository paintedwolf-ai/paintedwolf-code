package api

import "net/http"

// HandleSessionArtifactForTest exposes the artifact fetch handler for unit tests.
func (s *Server) HandleSessionArtifactForTest() http.HandlerFunc {
	return s.handleSessionArtifact
}

// HandleListSessionArtifactsForTest exposes session artifact enumeration for unit tests.
func (s *Server) HandleListSessionArtifactsForTest() http.HandlerFunc {
	return s.handleListSessionArtifacts
}

// HandleListProjectArtifactsForTest exposes project artifact enumeration for unit tests.
func (s *Server) HandleListProjectArtifactsForTest() http.HandlerFunc {
	return s.handleListProjectArtifacts
}

// HandleUploadLiveToolRecordingForTest exposes recording ingestion for unit tests.
func (s *Server) HandleUploadLiveToolRecordingForTest() http.HandlerFunc {
	return s.handleCreateSessionArtifact
}

// HandleDeleteProjectArtifactForTest exposes artifact deletion for unit tests.
func (s *Server) HandleDeleteProjectArtifactForTest() http.HandlerFunc {
	return s.handleDeleteProjectArtifact
}
