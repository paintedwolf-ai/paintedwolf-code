package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/api/extensionadmin"
	"github.com/lycaon/lycaon/internal/commandinvoke"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestFailedCommandReceiptUsesHTTPNoticeOnDeliveryAndReplay(t *testing.T) {
	s := NewServer(requiredTestDeps(t, Dependencies{UserNotices: testUserNotices(t)}), nil, TestAPIToken)
	status, body, handled := extensionadmin.OperationOutputFailure()
	if !handled {
		t.Fatal("failed operation lost its settled receipt")
	}
	for _, replay := range []bool{false, true} {
		if replay {
			encoded, err := commandinvoke.EncodeResponse(status, body)
			testutil.FailErr(t, "persist failed command receipt", err)
			status, body, err = commandinvoke.DecodeResponse(encoded)
			testutil.FailErr(t, "reload failed command receipt", err)
		}
		response := httptest.NewRecorder()
		s.Extensions.WriteCommandResponse(response, status, body)
		var notice wire.ErrorResponse
		testutil.FailErr(t, "decode HTTP error envelope", json.Unmarshal(response.Body.Bytes(), &notice))
		if response.Code != http.StatusBadGateway || notice.Code != "operation_output_invalid" || notice.Message == "" || notice.SuggestedAction == "" {
			t.Fatalf("failed receipt lost its notice: status=%d notice=%+v", response.Code, notice)
		}
	}
}

func TestSuccessfulCommandResponsesKeepTypedResults(t *testing.T) {
	s := NewServer(requiredTestDeps(t, Dependencies{}), nil, TestAPIToken)
	for _, status := range []int{http.StatusOK, http.StatusAccepted} {
		body := wire.CommandInvokeResponse{Status: "completed", Output: "result", FrameRevision: "captured-frame"}
		response := httptest.NewRecorder()
		s.Extensions.WriteCommandResponse(response, status, body)
		var decoded wire.CommandInvokeResponse
		testutil.FailErr(t, "decode command result", json.Unmarshal(response.Body.Bytes(), &decoded))
		if response.Code != status || decoded.Output != body.Output || decoded.FrameRevision != body.FrameRevision || decoded.Status != body.Status {
			t.Fatalf("command result changed: status=%d body=%+v", response.Code, decoded)
		}
	}
}
