package toolcontract

import (
	"strings"
	"testing"
)

// TestLiteralByteCapturesAreExactRevisions: a literal utility's captured
// bytes are a valid revision only when their identity is exact.
func TestLiteralByteCapturesAreExactRevisions(t *testing.T) {
	disk := strings.Repeat("a", 64)
	content := strings.Repeat("b", 64)
	base := ExecutionRevision{Role: SourceRevision, Basis: CapturedRevision, Path: "x", Representation: OriginalEncodedBytes, ContentBytes: 3}
	published := base
	published.Mode, published.DiskSHA256, published.ContentSHA256 = PublishedDiskRevision, disk, disk
	accepted := base
	accepted.Mode, accepted.DocumentID, accepted.DocumentRevision, accepted.DiskSHA256, accepted.ContentSHA256 = AcceptedDocumentRevision, "doc", 4, disk, content
	for name, revision := range map[string]ExecutionRevision{"published bytes": published, "accepted document bytes": accepted} {
		if err := revision.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	mismatched := published
	mismatched.ContentSHA256 = content
	if mismatched.Validate() == nil {
		t.Fatal("published bytes with a different content digest validated")
	}
	if pin, ok := accepted.SourcePin(); !ok || pin.Representation != OriginalEncodedBytes || pin.SHA256 != content || pin.EndByte != 3 {
		t.Fatalf("source pin = %+v", pin)
	}
}
