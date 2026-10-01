package bundled

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxSourceMembers       = 100000
	maxSourceBytes         = 8 << 30
	maxSourceMemberBytes   = 1 << 30
	maxSourceMetadataBytes = 16 << 20
)

type sourceGrammar struct {
	Language       string            `json:"language"`
	GeneratedFiles map[string]string `json:"generated_files"`
}
type sourceMember struct {
	SHA256  string  `json:"sha256,omitempty"`
	Bytes   *int64  `json:"bytes,omitempty"`
	Symlink *string `json:"symlink,omitempty"`
}
type sourceManifest struct {
	Identity struct {
		BaseRevision       string `json:"base_revision"`
		InterfacesRevision string `json:"interfaces_revision"`
		SourceLockSHA256   string `json:"source_lock_sha256"`
	} `json:"identity"`
	Files map[string]sourceMember `json:"files"`
}
type sourceProof struct {
	members   map[string]sourceMember
	documents map[string][]byte
}

var qualificationDocuments = [...]string{
	"source/tests/rule-translation.json", "source/tests/native-file-selection.json",
	"source/tests/callback-rule-validation.json", "source/tests/model-rule-validation.json", "source/tests/native-pattern-validation.json", "locks/runtimes.json",
}

func validArchivePath(name string) bool {
	return name != "" && name != "." && len(name) <= 4096 && !strings.ContainsAny(name, "\\\x00") && !strings.HasPrefix(name, "/") && name == path.Clean(name) && name != ".." && !strings.HasPrefix(name, "../")
}

func verifySourceArchive(m *Manifest, lock candidateSourceLock) (*sourceProof, error) {
	proof, raw, err := readSourceArchive(filepath.Join(m.artifactDirectory, "opengrep-source.tar.gz"))
	if err != nil {
		return nil, err
	}
	if candidateDigest(raw) != m.OpenGrep.SourceManifestSHA256 {
		return nil, fmt.Errorf("source archive manifest differs from reviewed source inventory")
	}
	var manifest sourceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("parse source manifest: %w", err)
	}
	if manifest.Identity.BaseRevision != lock.Revision || manifest.Identity.InterfacesRevision != lock.InterfacesRevision || manifest.Identity.SourceLockSHA256 != m.OpenGrep.SourceLockSHA256 {
		return nil, fmt.Errorf("source archive identity differs from source lock")
	}
	if len(manifest.Files) != len(proof.members) {
		return nil, fmt.Errorf("source archive membership differs from source manifest")
	}
	for name, expected := range manifest.Files {
		actual, exists := proof.members[name]
		if !exists || !sameSourceMember(expected, actual) {
			return nil, fmt.Errorf("source archive member differs from manifest: %s", name)
		}
	}
	if err := proof.verifyLockedInputs(m, lock); err != nil {
		return nil, err
	}
	for _, part := range m.identity.Payload {
		if part.Name == "LICENSE" {
			member := proof.members["engine/LICENSE"]
			if member.Bytes == nil || *member.Bytes != part.Bytes || member.SHA256 != part.SHA256 {
				return nil, fmt.Errorf("artifact license differs from corresponding source")
			}
		}
	}
	return proof, nil
}

func sameSourceMember(a, b sourceMember) bool {
	if a.Symlink != nil {
		return b.Symlink != nil && *a.Symlink == *b.Symlink && a.Bytes == nil && a.SHA256 == ""
	}
	return a.Bytes != nil && b.Bytes != nil && *a.Bytes == *b.Bytes && validHex(a.SHA256, 32) && a.SHA256 == b.SHA256 && b.Symlink == nil
}

func readSourceArchive(filename string) (*sourceProof, []byte, error) {
	// #nosec G304 -- fixed corresponding-source payload already checked against provenance.
	file, err := os.Open(filename)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = compressed.Close() }()
	archive := tar.NewReader(io.LimitReader(compressed, maxSourceBytes+1))
	proof := &sourceProof{members: make(map[string]sourceMember), documents: make(map[string][]byte)}
	seen := make(map[string]bool)
	var manifest []byte
	var total int64
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read source archive: %w", err)
		}
		if !validArchivePath(header.Name) || seen[header.Name] || len(seen) >= maxSourceMembers {
			return nil, nil, fmt.Errorf("invalid or duplicate source archive member: %s", header.Name)
		}
		seen[header.Name] = true
		total += header.Size
		if header.Size < 0 || header.Size > maxSourceMemberBytes || total > maxSourceBytes {
			return nil, nil, fmt.Errorf("source archive exceeds size bound")
		}
		if header.Name == "SOURCE-MANIFEST.json" {
			if header.Typeflag != tar.TypeReg || header.Size > maxSourceMetadataBytes {
				return nil, nil, fmt.Errorf("invalid source manifest entry")
			}
			manifest, err = readSourceDocument(archive, header.Size)
			if err != nil {
				return nil, nil, err
			}
			continue
		}
		member, raw, err := readSourceMember(archive, header)
		if err != nil {
			return nil, nil, err
		}
		proof.members[header.Name] = member
		if raw != nil {
			proof.documents[header.Name] = raw
		}
	}
	if manifest == nil {
		return nil, nil, fmt.Errorf("source archive manifest missing")
	}
	return proof, manifest, nil
}

func readSourceDocument(reader io.Reader, size int64) ([]byte, error) {
	if size > maxSourceMetadataBytes {
		return nil, fmt.Errorf("source metadata exceeds size bound")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != size {
		return nil, fmt.Errorf("source document size differs")
	}
	return raw, nil
}

func readSourceMember(reader io.Reader, header *tar.Header) (sourceMember, []byte, error) {
	if header.Typeflag == tar.TypeSymlink {
		// #nosec G305 -- normalize only; no extraction occurs, and escaping targets are rejected below.
		target := path.Clean(path.Join(path.Dir(header.Name), header.Linkname))
		if header.Linkname == "" || strings.HasPrefix(header.Linkname, "/") || !validArchivePath(target) || strings.ContainsAny(header.Linkname, "\\\x00") || header.Size != 0 {
			return sourceMember{}, nil, fmt.Errorf("source symlink escapes archive: %s", header.Name)
		}
		value := header.Linkname
		return sourceMember{Symlink: &value}, nil, nil
	}
	if header.Typeflag != tar.TypeReg {
		return sourceMember{}, nil, fmt.Errorf("unsupported source member type: %s", header.Name)
	}
	retain := header.Name == "inputs/source-lock.json"
	for _, name := range qualificationDocuments {
		if header.Name == "inputs/"+name {
			retain = true
		}
	}
	if retain {
		raw, err := readSourceDocument(reader, header.Size)
		if err != nil {
			return sourceMember{}, nil, err
		}
		size := header.Size
		return sourceMember{SHA256: candidateDigest(raw), Bytes: &size}, raw, nil
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, reader, header.Size); err != nil {
		return sourceMember{}, nil, err
	}
	size := header.Size
	return sourceMember{SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: &size}, nil, nil
}

func (proof *sourceProof) requireDigest(name, digest string) error {
	member, ok := proof.members[name]
	if !ok || member.Symlink != nil || member.Bytes == nil || !validHex(digest, 32) || member.SHA256 != digest {
		return fmt.Errorf("source archive lacks exact locked content: %s", name)
	}
	return nil
}

func (proof *sourceProof) verifyLockedInputs(m *Manifest, lock candidateSourceLock) error {
	if len(lock.Files) == 0 || !validHex(lock.InterfacesRevision, 20) {
		return fmt.Errorf("source lock inventory or interface revision missing")
	}
	if err := proof.requireDigest("inputs/source-lock.json", m.OpenGrep.SourceLockSHA256); err != nil {
		return err
	}
	for name, digest := range lock.Files {
		if !validArchivePath(name) {
			return fmt.Errorf("invalid locked input path: %s", name)
		}
		if err := proof.requireDigest("inputs/"+name, digest); err != nil {
			return err
		}
		if strings.HasPrefix(name, "source/") {
			if err := proof.requireDigest("engine/"+strings.TrimPrefix(name, "source/"), digest); err != nil {
				return err
			}
		}
	}
	for _, grammar := range lock.Grammars {
		if !validArchivePath(grammar.Language) || strings.Contains(grammar.Language, "/") {
			return fmt.Errorf("invalid locked grammar language")
		}
		for name, digest := range grammar.GeneratedFiles {
			if !validArchivePath(name) {
				return fmt.Errorf("invalid generated grammar path")
			}
			installed := name
			if path.Ext(name) == ".h" {
				installed = "include/" + name
			}
			if err := proof.requireDigest("engine/languages/native_scripts/"+grammar.Language+"/"+installed, digest); err != nil {
				return err
			}
		}
	}
	return nil
}
