package bundled

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ConsumerQualifier runs the product-owned qualification against prepared final bytes.
type ConsumerQualifier func(context.Context, *Manifest) (json.RawMessage, error)

// FinalizeConsumerArtifact preserves producer proof and emits authority only after qualification.
func FinalizeConsumerArtifact(ctx context.Context, producer *Manifest, root string, recipe ConsumerRecipe, qualify ConsumerQualifier) (*Manifest, error) {
	if err := ValidateManifest(producer); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if producer.identity == nil || producer.identity.Consumer != nil || producer.consumerPending || producer.identity.GOOS != "linux" || runtime.GOOS != "linux" || producer.identity.GOARCH != runtime.GOARCH || qualify == nil {
		return nil, fmt.Errorf("consumer finalization requires an original admitted artifact and native Linux qualifier")
	}
	if err := producer.verifyReleaseSelection(ctx); err != nil {
		return nil, err
	}
	if err := recipe.validate(); err != nil {
		return nil, err
	}
	if _, err := candidateRegularFile(recipe.ToolPath); err != nil {
		return nil, err
	}
	if err := VerifyFileSHA256(recipe.ToolPath, recipe.ToolSHA256); err != nil {
		return nil, fmt.Errorf("consumer recipe tool differs from its declaration: %w", err)
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("consumer finalization requires an explicit absolute staged root")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	directory := EngineBundledDir(root, producer.OpenGrep.Version)
	if directory == producer.artifactDirectory {
		return nil, fmt.Errorf("consumer finalization cannot mutate the producer artifact")
	}
	if _, err := os.Lstat(filepath.Join(directory, consumerRecordName)); !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("consumer finalization requires a fresh record destination")
	}
	binary := ArtifactPath(root, producer.OpenGrep.Version, "linux")
	part, err := identifyPayload(binary, "opengrep")
	if err != nil {
		return nil, err
	}
	if part.SHA256 == producer.identity.BinarySHA256 {
		return nil, fmt.Errorf("unchanged producer bytes require their original build identity")
	}
	platform, err := inspectConsumerELF(binary, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	identity := *producer.identity
	identity.Payload = append([]PayloadIdentity{}, producer.identity.Payload...)
	identity.BinarySHA256, identity.BinaryBytes = part.SHA256, part.Bytes
	prepared := &Manifest{OpenGrep: producer.OpenGrep, OpenGrepSelection: producer.OpenGrepSelection, identity: &identity,
		artifactDirectory: directory, consumerPending: true, consumerSource: producer, releaseAdmitted: true}
	if err := verifyArtifactDirectory(prepared, directory); err != nil {
		return nil, err
	}
	report, err := qualify(ctx, prepared)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyConsumerQualification(report, &identity); err != nil {
		return nil, err
	}
	if err := producer.verifyReleaseSelection(ctx); err != nil {
		return nil, err
	}
	if err := verifyArtifactDirectory(prepared, directory); err != nil {
		return nil, err
	}
	record := consumerRecord{SchemaVersion: 1, Producer: producer.identity, Recipe: recipe, Platform: platform,
		Executable: consumerExecutable{GOOS: identity.GOOS, GOARCH: identity.GOARCH, SHA256: part.SHA256, Bytes: part.Bytes}, Qualification: report}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if len(raw) > maxConsumerRecordBytes {
		return nil, fmt.Errorf("consumer finalization record exceeds its bound")
	}
	proof := PayloadIdentity{Name: consumerRecordName, SHA256: candidateDigest(raw), Bytes: int64(len(raw))}
	if _, err := installArtifactFile(root, filepath.Join(directory, consumerRecordName), proof.SHA256, proof.Bytes, 0o644, bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity.SchemaVersion = 2
	identity.Consumer = &ConsumerIdentity{ProducerSHA256: producer.identity.BinarySHA256, ProducerBytes: producer.identity.BinaryBytes, Record: proof}
	prepared.consumerPending = false
	if err := verifyArtifactDirectory(prepared, directory); err != nil {
		return nil, err
	}
	return prepared, nil
}

// SelectConsumerArtifact revalidates both the original archive and final embedded authority.
func SelectConsumerArtifact(ctx context.Context, producer *Manifest, directory, encoded string) (*Manifest, error) {
	if err := ValidateManifest(producer); err != nil {
		return nil, err
	}
	if producer.identity == nil || producer.identity.Consumer != nil {
		return nil, fmt.Errorf("consumer selection requires original producer authority")
	}
	if err := producer.verifyReleaseSelection(ctx); err != nil {
		return nil, err
	}
	selected, err := ManifestWithBuildIdentity(producer, encoded)
	if err != nil {
		return nil, err
	}
	if selected.identity.Consumer == nil || producer.identity.Consumer != nil {
		return nil, fmt.Errorf("consumer selection requires original producer and separate finalized identity")
	}
	selected.artifactDirectory, err = filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	selected.consumerSource, selected.releaseAdmitted = producer, true
	if err := verifyArtifactDirectory(selected, selected.artifactDirectory); err != nil {
		return nil, err
	}
	return selected, nil
}
