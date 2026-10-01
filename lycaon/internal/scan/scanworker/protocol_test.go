package scanworker

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestServeRejectsUnknownImplementationInResponse(t *testing.T) {
	input := strings.NewReader(`{"impl":"unknown","id":"scanner","jobs":1,"scan":{}}`)
	var output bytes.Buffer
	if err := Serve(context.Background(), input, &output); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var response Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(response.Error, "unknown library scanner implementation") {
		t.Fatalf("error = %q", response.Error)
	}
}

func TestServeRejectsUnknownRequestFields(t *testing.T) {
	input := strings.NewReader(`{"impl":"unknown","id":"scanner","jobs":1,"scan":{},"extra":true}`)
	if err := Serve(context.Background(), input, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown request field must fail")
	}
}
