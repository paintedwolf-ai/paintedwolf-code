package scanworker

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestServeAnswersEveryRequestLineUntilInputCloses(t *testing.T) {
	input := strings.NewReader(`{"impl":"nope","id":"a","jobs":1,"scan":{"ProjectDir":"/tmp","Categories":null,"ScannerID":"","Paths":null,"FileTimeout":0}}` + "\n" +
		`{"impl":"nope","id":"b","jobs":1,"scan":{"ProjectDir":"/tmp","Categories":null,"ScannerID":"","Paths":null,"FileTimeout":0}}` + "\n")
	var output bytes.Buffer
	if err := Serve(context.Background(), input, &output); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("responses = %d, want one per request: %q", len(lines), output.String())
	}
	for _, line := range lines {
		var response Response
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		if response.Error == "" || response.Result != nil {
			t.Fatalf("unknown implementation must answer with an error: %+v", response)
		}
	}
}
