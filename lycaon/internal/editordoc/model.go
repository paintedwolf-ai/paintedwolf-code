// Package editordoc manages editor drafts and filesystem saves.
package editordoc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	ErrReadOnly           = errors.New("editor source is read-only")
	ErrNotFound           = errors.New("editor document not found")
	ErrInvalidEOL         = errors.New("editor line ending must be lf or crlf")
	ErrRevisionConflict   = errors.New("editor document revision conflict")
	ErrRootDetached       = errors.New("editor document root is not attached")
	// ErrOperationConflict rejects operation reuse with different input.
	ErrOperationConflict = errors.New("editor save operation id was already used for different input")
)

type Document struct {
	SaveRevision                             int64 `json:"-"`
	CommandHistory                           *CommandHistory
	historyVector                            []byte
	BranchID                                 sourcebranch.ID
	WorkspaceID                              string
	authorship                               authorshipContext
	pinnedCheckpoint                         []byte
	publishedCheckpoint                      []byte
	filesystemClient                         uint32
	plannedEdits                             []documentcore.Edit
	ReplicaID                                uint32
	Participants                             []Participant
	replicaCommit                            *replicaCommit
	Epoch                                    int64
	StateVector                              []byte
	CRDTUpdate                               []byte
	PublishedRevision                        int64
	ID, ProjectID, FileID, RootID, Path      string
	Draft, BaseContent, BaseSHA256, Encoding string
	SizeBytes                                int64
	EOL, BaseEOL                             string
	MixedEOL, BaseMixedEOL, Dirty            bool
	Revision                                 int64
	CreatedAt, UpdatedAt                     time.Time
	Diverged                                 bool
	// Absent says the path has no file on disk; the draft and saved base stay.
	Absent bool
	// HeldAgentVersionID retains an unsaved agent edit until a save settles it.
	HeldAgentVersionID string
}

// HoldsDraft reports whether the document carries text the file does not.
func (d *Document) HoldsDraft() bool { return d.Dirty || d.HeldAgentVersionID != "" }

// DraftSHA256 hashes the encoded draft or reuses the clean file's hash.
func (d *Document) DraftSHA256() (string, error) {
	if !d.Dirty {
		return d.BaseSHA256, nil
	}
	encoded, err := textfile.EncodeBounded(serializeEOL(d.Draft, d.EOL), d.Encoding, textfile.LimitsForRaw(projectsource.SourceWriteMaxBytes))
	if err != nil {
		return "", err
	}
	return textfile.SHA256(encoded), nil
}

// Mutation journals a save with explicit user or agent attribution.
type Mutation struct {
	ClientID                                                    string
	ReplayCompacted                                             bool
	BranchID                                                    sourcebranch.ID
	BeforeCheckpoint                                            []byte
	Checkpoint                                                  []byte
	ID, DocumentID, ProjectID, FileID, RootID, Path             string
	InputDigest, ExpectedSHA256, AfterSHA256, Encoding, Content string
	BeforeBytes, AfterBytes                                     []byte
	Status, SessionID, Error, ResponseJSON                      string
	Turn                                                        int
	CreatedAt, UpdatedAt                                        time.Time
	DraftRevision                                               int64
	EOL                                                         string
	Origin                                                      api.SourceChangeOrigin
	// PersonID is the person who saved; agent saves have none.
	PersonID             string
	ToolCallID, ToolName string
}

type saveActor struct {
	Origin     api.SourceChangeOrigin
	PersonID   string
	ClientID   string
	SessionID  string
	Turn       int
	ToolCallID string
	ToolName   string
}

func saveInputDigest(documentID, projectID string, actor saveActor, expected int64) string {
	turn := actor.Turn
	// User affiliation is captured at admission; a retry may arrive in a later turn.
	if actor.Origin == api.SourceChangeOriginUser {
		turn = 0
	}
	raw := strings.Join([]string{
		documentID, projectID, string(actor.Origin), actor.PersonID, actor.ClientID, actor.SessionID,
		actor.ToolCallID, actor.ToolName,
		strconv.Itoa(turn), strconv.FormatInt(expected, 10),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func rootPath(p *project.Project, rootID string) string {
	for _, root := range p.Roots {
		if root.ID == rootID {
			return fspath.CanonicalPath(root.Path)
		}
	}
	return ""
}

func normalizeEOL(content string) (string, string, bool) {
	crlf := strings.Count(content, "\r\n")
	withoutCRLF := strings.ReplaceAll(content, "\r\n", "")
	lf := strings.Count(withoutCRLF, "\n")
	eol := "lf"
	if crlf > 0 && lf == 0 {
		eol = "crlf"
	}
	return strings.ReplaceAll(content, "\r\n", "\n"), eol, crlf > 0 && lf > 0
}

func serializeEOL(content, eol string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if eol == "crlf" {
		return strings.ReplaceAll(content, "\n", "\r\n")
	}
	return content
}
