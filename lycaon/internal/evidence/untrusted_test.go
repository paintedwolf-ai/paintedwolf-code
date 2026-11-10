package evidence

import (
	"slices"
	"testing"
)

func TestRecordMarksUntrustedContent(t *testing.T) {
	cases := []struct {
		name string
		rec  Record
		want bool
	}{
		{"web_search", Record{SourceTool: "web_search"}, true},
		{"fetch_url", Record{SourceTool: "fetch_url"}, true},
		{"mcp", Record{SourceTool: "mcp_docs_get"}, true},
		{"inherit marker", Record{Handle: InheritUntrustedHandle}, true},
		{"worker url marker", Record{Handle: WorkerURLHandlePrefix + "deadbeef"}, true},
		{"namespaced inherit marker", Record{Handle: "root:child:" + InheritUntrustedHandle}, true},
		{"namespaced worker url marker", Record{Handle: "root:child:" + WorkerURLHandlePrefix + "deadbeef"}, true},
		{"read", Record{SourceTool: "read"}, false},
		{"read with untrusted content", Record{SourceTool: "read", UntrustedContent: true}, true},
		{"http_request inline external", Record{SourceTool: "http_request", UntrustedContent: true}, true},
		{"http_request file custody only", Record{SourceTool: "http_request", ExternalCustody: true}, false},
		{"command external output", Record{SourceTool: "command", UntrustedContent: true}, true},
		{"empty", Record{}, false},
		{"trust ignored", Record{SourceTool: "web_search", Fidelity: FidelityStructured}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RecordMarksUntrustedContent(tc.rec); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestBuildEvidenceRecordHTTPRequestProvenance(t *testing.T) {
	projectDir := t.TempDir()

	// 1. External inline body marks UntrustedContent and records ObservedURLs.
	inlineRec := BuildEvidenceRecord(
		projectDir,
		"http_request",
		map[string]any{"url": "https://api.external.example/v1/items"},
		`{"status":200,"url":"https://cdn.external.example/v1/items","body":"hello"}`,
	)
	if !RecordMarksUntrustedContent(inlineRec) {
		t.Fatalf("expected inline external http_request to mark untrusted content: %+v", inlineRec)
	}
	if gotURLs := ObservedURLsForRecord(inlineRec); len(gotURLs) != 2 {
		t.Fatalf("expected 2 observed URLs, got %v", gotURLs)
	}

	// 2. External response_path records ExternalCustody on the file without marking model ingestion.
	fileRec := BuildEvidenceRecord(
		projectDir,
		"http_request",
		map[string]any{"url": "https://api.external.example/v1/archive.txt", "response_path": "saved.txt"},
		`{"status":200,"url":"https://api.external.example/v1/archive.txt","response_path":"saved.txt"}`,
	)
	if RecordMarksUntrustedContent(fileRec) {
		t.Fatalf("expected response_path http_request not to mark model ingestion on download: %+v", fileRec)
	}
	if !fileRec.ExternalCustody || fileRec.Path != "saved.txt" {
		t.Fatalf("expected ExternalCustody on saved.txt, got %+v", fileRec)
	}

	// 3. Discard body does not mark model ingestion.
	discardRec := BuildEvidenceRecord(
		projectDir,
		"http_request",
		map[string]any{"url": "https://api.external.example/v1/ping", "response_body": "discard"},
		`{"status":200,"url":"https://api.external.example/v1/ping"}`,
	)
	if RecordMarksUntrustedContent(discardRec) {
		t.Fatalf("expected discard http_request not to mark untrusted content: %+v", discardRec)
	}

	// 4. Local loopback and unix_socket requests do not mark external untrusted content.
	loopbackRec := BuildEvidenceRecord(
		projectDir,
		"http_request",
		map[string]any{"url": "http://127.0.0.1:8080/health"},
		`{"status":200,"url":"http://127.0.0.1:8080/health","body":"ok"}`,
	)
	if RecordMarksUntrustedContent(loopbackRec) || loopbackRec.ExternalCustody {
		t.Fatalf("expected loopback http_request to stay clean, got %+v", loopbackRec)
	}

	unixRec := BuildEvidenceRecord(
		projectDir,
		"http_request",
		map[string]any{"url": "http://docker/info", "unix_socket": "/tmp/daemon.sock"},
		`{"status":200,"url":"http://docker/info","body":"ok"}`,
	)
	if RecordMarksUntrustedContent(unixRec) || len(ObservedURLsForRecord(unixRec)) != 0 {
		t.Fatalf("expected unix_socket http_request to stay clean, got %+v", unixRec)
	}
}

func TestRecordAuxRoundTripCompatibilityAndExternalCustodySupersession(t *testing.T) {
	// 1. Old persisted records without untrusted_content/external_custody fields round-trip cleanly.
	var legacyRec Record
	ApplyRecordAux(&legacyRec, RecordAux{PathsTouched: []string{"saved.txt"}})
	if legacyRec.UntrustedContent || legacyRec.ExternalCustody || !slices.Equal(IndexedPathsForRecord(legacyRec), []string{"saved.txt"}) {
		t.Fatalf("unexpected legacy decode: %+v", legacyRec)
	}

	// 2. New provenance flags round-trip through CloneRecordForStore / ApplyRecordAux.
	orig := Record{
		Kind:             "http_response",
		Shape:            ShapeSurfaceSnapshot,
		SourceTool:       "http_request",
		Path:             "saved.txt",
		UntrustedContent: true,
		ExternalCustody:  true,
	}
	orig.touchURL("https://api.external.example/v1/data")
	orig.SetSeq(12)
	aux := CloneRecordForStore(orig)
	var decoded Record
	ApplyRecordAux(&decoded, aux)
	if !decoded.UntrustedContent || !decoded.ExternalCustody || decoded.Seq() != 12 || !slices.Contains(ObservedURLsForRecord(decoded), "https://api.external.example/v1/data") {
		t.Fatalf("round-trip lost provenance or seq: %+v (seq=%d)", decoded, decoded.Seq())
	}

	// 3. Partial local mutation (edit) preserves external custody, whereas full local replacement (write/delete) supersedes it.
	dl1 := Record{Handle: "http_response#1", Kind: "http_response", Shape: ShapeSurfaceSnapshot, SourceTool: "http_request", Path: "saved.txt", ExternalCustody: true}
	dl1.touchURL("https://first.external.example/a")
	partialEdit := Record{Handle: "write#1", Kind: "write", Shape: ShapeStructuredEvent, SourceTool: "edit", Path: "saved.txt"}
	ledgerAfterEdit := AssembleLedger([]Record{dl1, partialEdit})
	if urls := ExternalCustodyURLs(ledgerAfterEdit, []string{"saved.txt"}); !slices.Equal(urls, []string{"https://first.external.example/a"}) {
		t.Fatalf("partial edit must preserve external custody URLs, got %v", urls)
	}

	fullWrite := Record{Handle: "write#2", Kind: "write", Shape: ShapeStructuredEvent, SourceTool: "write", Path: "saved.txt"}
	ledgerAfterFullWrite := AssembleLedger([]Record{dl1, partialEdit, fullWrite})
	if urls := ExternalCustodyURLs(ledgerAfterFullWrite, []string{"saved.txt"}); len(urls) != 0 {
		t.Fatalf("full local write must clear older external custody URLs, got %v", urls)
	}

	// 4. A subsequent external download after a full local write attaches only the new URL, not stale prior URLs.
	dl2 := Record{Handle: "http_response#2", Kind: "http_response", Shape: ShapeSurfaceSnapshot, SourceTool: "http_request", Path: "saved.txt", ExternalCustody: true}
	dl2.touchURL("https://second.external.example/b")
	ledgerAfterRedownload := AssembleLedger([]Record{dl1, partialEdit, fullWrite, dl2})
	if urls := ExternalCustodyURLs(ledgerAfterRedownload, []string{"saved.txt"}); !slices.Equal(urls, []string{"https://second.external.example/b"}) {
		t.Fatalf("re-download after full write must carry only new URL, got %v", urls)
	}
}

