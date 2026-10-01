package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPerformanceResponseWriterKeepsFirstStatus(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &performanceResponseWriter{ResponseWriter: recorder, status: http.StatusOK}
	writer.WriteHeader(http.StatusAccepted)
	writer.WriteHeader(http.StatusInternalServerError)

	if writer.status != http.StatusAccepted {
		t.Fatalf("recorded status = %d, want first status %d", writer.status, http.StatusAccepted)
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("response status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
}

func TestPerformanceResponseWriterRecordsImplicitOK(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &performanceResponseWriter{ResponseWriter: recorder, status: http.StatusOK}
	if _, err := writer.Write([]byte("ready")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if writer.firstWrite.IsZero() || writer.status != http.StatusOK {
		t.Fatalf("implicit response = status %d first=%v", writer.status, writer.firstWrite)
	}
}
