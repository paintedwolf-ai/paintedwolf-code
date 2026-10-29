package toolcontract

import (
	"encoding/hex"
	"fmt"
	"strconv"
)

// ExecutionRevision is what one program operation captured for one path before
// preparing its effect. It is execution evidence only: it never establishes a
// conversational read base, and a changed revision conflicts instead of
// refreshing, rebasing or rerunning the operation.
type ExecutionRevision struct {
	Role  ExecutionRevisionRole
	Mode  ExecutionRevisionMode
	Basis ExecutionRevisionBasis
	// RootID and Path name the logical project location; Path is root-relative.
	// A granted path outside every root has no RootID and an absolute Path.
	RootID, Path string
	// DocumentID and DocumentRevision identify the accepted document snapshot.
	DocumentID       string
	DocumentRevision int64
	// DiskSHA256 hashes the published file bytes; empty when the path is absent.
	DiskSHA256 string
	// ContentSHA256 hashes the exact captured content in Representation: the
	// LF document text, decoded file text, or the raw bytes declared to a launch.
	Representation SourceRepresentation
	ContentSHA256  string
	ContentBytes   int64
}

type ExecutionRevisionRole string

const (
	SourceRevision      ExecutionRevisionRole = "source"
	DestinationRevision ExecutionRevisionRole = "destination"
)

// ExecutionRevisionMode says which owner served the captured state. Native
// reads use the accepted document when one exists; external processes read
// published disk.
type ExecutionRevisionMode string

const (
	AcceptedDocumentRevision ExecutionRevisionMode = "accepted_document"
	PublishedDiskRevision    ExecutionRevisionMode = "published_disk"
	AbsentRevision           ExecutionRevisionMode = "absent"
)

// ExecutionRevisionBasis separates bytes a native operation actually used from
// the identity an external process could read at launch. Declared inputs are
// not a claim that the process read them, or read nothing else.
type ExecutionRevisionBasis string

const (
	CapturedRevision ExecutionRevisionBasis = "captured"
	DeclaredAtLaunch ExecutionRevisionBasis = "declared_at_launch"
)

func (r ExecutionRevision) Validate() error {
	switch r.Role {
	case SourceRevision, DestinationRevision:
	default:
		return fmt.Errorf("execution revision requires a source or destination role")
	}
	if r.Path == "" {
		return fmt.Errorf("execution revision requires a logical path")
	}
	switch r.Basis {
	case CapturedRevision:
	case DeclaredAtLaunch:
		if r.Mode == AcceptedDocumentRevision {
			return fmt.Errorf("external launch inputs are published disk state")
		}
	default:
		return fmt.Errorf("execution revision requires a captured or declared basis")
	}
	switch r.Mode {
	case AcceptedDocumentRevision:
		if r.DocumentID == "" || r.DocumentRevision <= 0 {
			return fmt.Errorf("accepted document revision requires its document identity")
		}
		if r.DiskSHA256 != "" && !isSHA256(r.DiskSHA256) {
			return fmt.Errorf("accepted document revision has an invalid published base")
		}
	case PublishedDiskRevision:
		if r.DocumentID != "" || r.DocumentRevision != 0 || !isSHA256(r.DiskSHA256) {
			return fmt.Errorf("published disk revision requires exact file bytes and no document")
		}
	case AbsentRevision:
		if r.Role != DestinationRevision || r.DocumentID != "" || r.DiskSHA256 != "" || r.ContentSHA256 != "" || r.ContentBytes != 0 {
			return fmt.Errorf("absence is a destination precondition without content")
		}
		return nil
	default:
		return fmt.Errorf("unknown execution revision mode %q", r.Mode)
	}
	representation := r.Representation == LogicalUTF8LF || r.Representation == UTF8SourceBytes
	if r.Basis == DeclaredAtLaunch {
		representation = r.Representation == OriginalEncodedBytes && r.ContentSHA256 == r.DiskSHA256
	}
	if !representation || !isSHA256(r.ContentSHA256) || r.ContentBytes < 0 {
		return fmt.Errorf("execution revision requires exact captured content identity")
	}
	return nil
}

// Same reports whether two captures name the same state of the same path; the
// role and basis of a capture are not part of the state.
func (r ExecutionRevision) Same(other ExecutionRevision) bool {
	return r.RootID == other.RootID && r.Path == other.Path && r.Mode == other.Mode &&
		r.DocumentID == other.DocumentID && r.DocumentRevision == other.DocumentRevision &&
		r.DiskSHA256 == other.DiskSHA256 && r.Representation == other.Representation &&
		r.ContentSHA256 == other.ContentSHA256 && r.ContentBytes == other.ContentBytes
}

// SourcePin projects a captured source into the stream-mapping identity. An
// absent destination has no content to pin.
func (r ExecutionRevision) SourcePin() (SourcePin, bool) {
	switch r.Mode {
	case AcceptedDocumentRevision:
		return SourcePin{SourceID: "document:" + r.DocumentID, Revision: strconv.FormatInt(r.DocumentRevision, 10),
			Representation: r.Representation, SHA256: r.ContentSHA256, EndByte: r.ContentBytes}, true
	case PublishedDiskRevision:
		return SourcePin{SourceID: "disk:" + r.RootID + "/" + r.Path, Revision: "sha256:" + r.DiskSHA256,
			Representation: r.Representation, SHA256: r.ContentSHA256, EndByte: r.ContentBytes}, true
	}
	return SourcePin{}, false
}

func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	var digest [32]byte
	_, err := hex.Decode(digest[:], []byte(s))
	return err == nil
}
