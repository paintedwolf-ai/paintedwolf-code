// Package sourcesnapshot publishes immutable manifests of admitted files. A
// manifest identifies content; it copies no bytes.
package sourcesnapshot

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/lycaon/lycaon/internal/sourcescope"
)

// Root is one filesystem tree in a snapshot, identified by canonical path.
type Root struct {
	Path string
}

// Identity says where an entry's content id came from.
type Identity string

const (
	// IdentityIndex is git's own blob id for a file clean against the index.
	// The bytes are in git's object store; the file was not read.
	IdentityIndex Identity = "index"
	// IdentityHashed is a digest the host computed by reading the file.
	IdentityHashed Identity = "hashed"
	// IdentityStat is a file too large to hash, identified by its stat facts.
	IdentityStat Identity = "stat"
)

// Entry is one immutable regular file as a generation identified it.
type Entry struct {
	RootPath string
	Path     string
	// SHA256 is the raw content digest; empty unless the host read the file.
	SHA256 string
	// GitOID is the git sha1 blob id; empty only for a stat identity.
	GitOID     string
	Identity   Identity
	Size       int64
	Mode       uint32
	ModifiedNS int64
}

// ContentID names the bytes, or nothing for a stat identity. The git id
// comes first: every route that reads a file derives it, so one file has
// one id however it was identified.
func (e Entry) ContentID() string {
	switch {
	case e.GitOID != "":
		return "git:" + e.GitOID
	case e.SHA256 != "":
		return "sha256:" + e.SHA256
	}
	return ""
}

// SameContent compares shared digests, or metadata for two stat identities.
func (e Entry) SameContent(other Entry) bool {
	switch {
	case e.GitOID != "" && other.GitOID != "":
		return e.GitOID == other.GitOID
	case e.SHA256 != "" && other.SHA256 != "":
		return e.SHA256 == other.SHA256
	case e.Identity == IdentityStat && other.Identity == IdentityStat:
		return e.statKey() == other.statKey()
	}
	return false
}

// contentKey is what the manifest hash sees: the content id, or the stat
// facts when there is none.
func (e Entry) contentKey() string {
	if id := e.ContentID(); id != "" {
		return id
	}
	return "stat:" + e.statKey()
}

func (e Entry) statKey() string {
	return strconv.FormatInt(e.Size, 10) + ":" + strconv.FormatUint(uint64(e.Mode), 10) + ":" + strconv.FormatInt(e.ModifiedNS, 10)
}

// statMatches compares live metadata with the recorded size, mode, and mtime.
func (e Entry) statMatches(info os.FileInfo) bool {
	return info != nil && info.Size() == e.Size && uint32(info.Mode()) == e.Mode && info.ModTime().UnixNano() == e.ModifiedNS
}

// Change is one path that differs between two generations: Before is nil
// for a file the newer one added, After is nil for one it dropped.
type Change struct {
	Before *Entry
	After  *Entry
}

// Path names the changed file.
func (c Change) Path() string {
	if c.After != nil {
		return c.After.Path
	}
	return c.Before.Path
}

// RootPath names the changed file's root.
func (c Change) RootPath() string {
	if c.After != nil {
		return c.After.RootPath
	}
	return c.Before.RootPath
}

// ErrContentUnavailable means no source still holds an entry's bytes: the
// revision store never captured them, git has no such object, and the live
// file has moved on.
var ErrContentUnavailable = errors.New("source content is no longer available")

// fileModeFromDB converts a persisted mode int64 into FileMode bits.
func fileModeFromDB(mode int64) (uint32, error) {
	if mode < 0 || mode > int64(^uint32(0)) {
		return 0, fmt.Errorf("file mode %d out of uint32 range", mode)
	}
	return uint32(mode), nil
}

// CaptureQuality reports how an observation was gathered.
type CaptureQuality string

const (
	// CaptureExact means a confirming pass found no changes.
	CaptureExact CaptureQuality = "exact"
	// CaptureObserved means the tree did not settle between passes.
	CaptureObserved CaptureQuality = "observed"
)

// Normalized defaults to CaptureObserved, the weaker claim.
func (q CaptureQuality) Normalized() CaptureQuality {
	if q == CaptureExact {
		return CaptureExact
	}
	return CaptureObserved
}

// AdmissionMode records how much of the tree the scope let a publication
// observe. It is observation provenance, not snapshot identity.
type AdmissionMode string

const (
	// AdmissionScope means every directory the scope admitted was observed.
	// Directories the floor, the project, or an ignore file left out are
	// boundaries by policy, not by cost.
	AdmissionScope AdmissionMode = "scope"
	// AdmissionScopeBounded means a budget stopped the observation short of
	// what the scope admitted. The boundaries name where.
	AdmissionScopeBounded AdmissionMode = "scope_bounded"
)

// Boundary is one directory a publication did not enter.
type Boundary struct {
	RootPath string
	// Path is slash-relative to the root; "." is the root itself.
	Path string
	// Reason is the walk's reason: a budget name, or "scope".
	Reason string
	// Detail is the scope's reason for a scope boundary: "floor", "declared",
	// or "ignored".
	Detail string
	// Entries counts what was read at or below Path before the cut; zero when
	// the directory was never opened.
	Entries int
}

// Budgeted reports whether a cost bound, rather than policy, drew the boundary.
func (b Boundary) Budgeted() bool {
	switch b.Reason {
	case "directory_cap", "subtree_cap", "walk_budget":
		return true
	default:
		return false
	}
}

// Snapshot is an atomically published source generation whose entries stream
// from the store.
type Snapshot struct {
	ID           string
	RootsKey     string
	MerkleSHA256 string
	FileCount    int
	TotalBytes   int64
	Quality      CaptureQuality
	// AdmissionMode records how the present-set was selected for this
	// publication attempt. It is observation provenance, not snapshot identity.
	AdmissionMode AdmissionMode
	CreatedAt     time.Time
	Roots         []Root
	// Boundaries are the directories this generation did not observe.
	Boundaries []Boundary
}

// Unobserved returns coverage gaps from observation caps or unreadable directories.
func (s Snapshot) Unobserved() []Boundary {
	var out []Boundary
	for _, b := range s.Boundaries {
		if b.Reason != "scope" {
			out = append(out, b)
		}
	}
	return out
}

// Verify selects how much of the tree one publish re-reads.
type Verify string

const (
	// VerifyStat reuses digests when file metadata is unchanged.
	VerifyStat Verify = "stat"
	// VerifyContent re-hashes every admitted file.
	VerifyContent Verify = "content"
)

// Normalized returns a recognized mode, defaulting to VerifyStat.
func (v Verify) Normalized() Verify {
	if v == VerifyContent {
		return VerifyContent
	}
	return VerifyStat
}

// Request identifies one source generation to publish.
type Request struct {
	Roots []Root
	// Verify selects the publish's re-read policy. Zero means VerifyStat.
	Verify Verify
	// Scope, when set, replaces the store's capture scope for every root in
	// this request. Tests and single-purpose captures use it; the host's
	// publications leave it nil.
	Scope *sourcescope.Scope
}
