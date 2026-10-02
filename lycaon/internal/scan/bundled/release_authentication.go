package bundled

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// AttestedReleaseSelection authenticates new trust roots; normal builds use the reviewed manifest.
type AttestedReleaseSelection struct {
	Descriptor, Tag, ExpectedCommit, Destination string
	GOOS, GOARCH                                 string
	Fetch                                        ReleaseFetchOptions
}

var attestedReleaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+\+paintedwolf\.[1-9][0-9]*$`)

// SelectAttestedRelease verifies both publisher and immutable-release attestations before changing a pin.
func SelectAttestedRelease(ctx context.Context, selection AttestedReleaseSelection) error {
	if err := selection.validate(); err != nil {
		return err
	}
	run, err := githubReleaseCommand()
	if err != nil {
		return err
	}
	return selectAttestedRelease(ctx, selection, run)
}

func (selection AttestedReleaseSelection) validate() error {
	if !attestedReleaseTag.MatchString(selection.Tag) || !validHex(selection.ExpectedCommit, 20) || strings.ToLower(selection.ExpectedCommit) != selection.ExpectedCommit {
		return fmt.Errorf("selection requires an explicit version tag and independently reviewed full lowercase source commit")
	}
	if selection.Descriptor == "" || selection.Destination == "" || selection.Fetch.CacheRoot == "" || selection.Fetch.Offline {
		return fmt.Errorf("authenticated selection requires descriptor, destination, cache and online release verification")
	}
	if strings.Contains(selection.Descriptor, "://") {
		return checkReleaseAssetURL(selection.Descriptor, selection.Tag, "release.json")
	}
	return nil
}

func selectAttestedRelease(ctx context.Context, selection AttestedReleaseSelection, run releaseCommand) error {
	state, err := checkImmutableRelease(ctx, run, selection.Tag)
	if err != nil {
		return err
	}
	raw, err := readAttestedDescriptor(ctx, selection)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "opengrep-selection-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: directory, Rel: "release.json"}, Source: bytes.NewReader(raw), Mode: 0o600})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	evidence, err := authenticateReleaseFile(ctx, run, filepath.Join(directory, "release.json"), hex.EncodeToString(digest[:]), selection.Tag, selection.ExpectedCommit)
	if err != nil {
		return fmt.Errorf("authenticate release descriptor: %w", err)
	}
	source, err := ParseReleaseManifest(raw)
	if err != nil {
		return err
	}
	if selection.Tag != "v"+source.OpenGrep.Version {
		return fmt.Errorf("authenticated descriptor version differs from selected tag")
	}
	stable, err := stableReleaseState(state)
	if err != nil {
		return err
	}
	receipt := releaseSelectionEvidence{Tag: selection.Tag, Commit: selection.ExpectedCommit, Descriptor: raw, DescriptorEvidence: evidence, ReleaseState: stable}
	for _, pin := range source.OpenGrep.Artifacts {
		proof, err := authenticateReleaseArchive(ctx, selection, run, pin)
		if err != nil {
			return err
		}
		receipt.Artifacts = append(receipt.Artifacts, proof)
	}
	resolved, err := FetchReleaseArtifact(ctx, source, selection.GOOS, selection.GOARCH, selection.Fetch)
	if err != nil {
		return err
	}
	reference, err := persistReleaseEvidence(selection.Destination, receipt)
	if err != nil {
		return err
	}
	resolved.Manifest.OpenGrepSelection = reference
	if err := ctx.Err(); err != nil {
		return err
	}
	return WriteReleaseManifest(ctx, resolved.Manifest, selection.Destination)
}

func readAttestedDescriptor(ctx context.Context, selection AttestedReleaseSelection) ([]byte, error) {
	var reader io.ReadCloser
	if strings.Contains(selection.Descriptor, "://") {
		response, err := openReleaseURL(ctx, selection.Descriptor, selection.Fetch.Client)
		if err != nil {
			return nil, err
		}
		defer func() { _ = response.Body.Close() }()
		reader = response.Body
	} else {
		file, err := openReleaseFile(selection.Descriptor)
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		reader = file
	}
	raw, err := io.ReadAll(io.LimitReader(&releaseContextReader{ctx: ctx, reader: reader}, maxReleaseManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > maxReleaseManifestBytes {
		return nil, fmt.Errorf("release descriptor size is invalid")
	}
	return raw, nil
}

func checkReleaseAssetURL(location, tag, name string) error {
	u, err := url.Parse(location)
	if err != nil {
		return err
	}
	prefix := "/" + releaseRepository + "/releases/download/" + tag + "/"
	if u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, prefix) || strings.Contains(strings.TrimPrefix(u.Path, prefix), "/") || strings.TrimPrefix(u.Path, prefix) == "" {
		return fmt.Errorf("artifact URL must name the selected repository release asset")
	}
	if name != "" && u.Path != prefix+name {
		return fmt.Errorf("unexpected release asset name")
	}
	return nil
}

func authenticateReleaseArchive(ctx context.Context, selection AttestedReleaseSelection, run releaseCommand, pin ReleaseArtifact) (releaseSubjectEvidence, error) {
	var proof releaseSubjectEvidence
	if err := checkReleaseAssetURL(pin.URL, selection.Tag, ""); err != nil {
		return proof, err
	}
	root, err := releaseCacheRoot(selection.Fetch.CacheRoot)
	if err != nil {
		return proof, err
	}
	lock, err := lockReleaseCache(ctx, root, pin.SHA256)
	if err != nil {
		return proof, err
	}
	defer func() { _ = lock.Close() }()
	options := selection.Fetch
	if pin.GOOS != selection.GOOS || pin.GOARCH != selection.GOARCH {
		options.Archive = ""
	}
	archive, err := ensureReleaseArchive(ctx, root, &pin, options)
	if err != nil {
		return proof, err
	}
	defer func() { _ = archive.file.Close() }()
	return authenticateReleaseFile(ctx, run, archive.file.Name(), pin.SHA256, selection.Tag, selection.ExpectedCommit)
}
