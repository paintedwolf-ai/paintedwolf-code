package backup

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

// The captured database determines which bodies the archive needs.
const retainedFilesQuery = `
SELECT 'source', '', content_sha256 FROM source_versions
WHERE capture_state = 'stored' AND content_sha256 != ''
UNION SELECT 'source', '', sha256 FROM worker_baseline_objects
UNION SELECT 'source', '', sha256 FROM source_command_window_objects
UNION SELECT 'source', '', sha256 FROM checkpoint_object_refs
UNION SELECT 'source', '', sha256 FROM source_recovery_objects
UNION SELECT 'model', project_id, content_blob_sha256 FROM model_outputs WHERE content_blob_sha256 != ''
UNION SELECT 'model', project_id, content_blob_sha256 FROM evidence_records WHERE content_blob_sha256 != ''
UNION SELECT 'artifact', project_id, content_hash FROM artifacts WHERE deleted_at IS NULL
UNION SELECT 'attachment', project_id, blob_id FROM message_attachment_refs
UNION SELECT 'attachment', project_id, blob_id FROM prompt_attachment_admissions
UNION SELECT 'spill', project_id, rel_path FROM message_spill_refs
UNION SELECT 'spill', project_id, rel_path FROM compaction_spill_refs
UNION SELECT 'baseline', '', b.id FROM worker_baselines b WHERE EXISTS (SELECT 1 FROM worker_jobs j WHERE j.workspace_baseline_id=b.id OR j.workspace_overlay_id=b.id)
UNION SELECT 'branch-meta', '', workspace_relpath || '.meta/state.json' FROM worker_jobs WHERE workspace_relpath IS NOT NULL AND workspace_relpath != ''
`

func validateArchiveReferences(ctx context.Context, snapshot db.DBTX, files map[string]archiveSource) error {
	rows, err := snapshot.QueryContext(ctx, retainedFilesQuery)
	if err != nil {
		return fmt.Errorf("backup: read retained file references: %w", err)
	}
	defer func() { _ = rows.Close() }()
	// SQLite retains attachment ids; each blob directory supplies its filename.
	attachments := make(map[string]int)
	for rel, source := range files {
		if source.kind == fileKindRegular {
			attachments[filepath.ToSlash(filepath.Dir(rel))]++
		}
	}
	for rows.Next() {
		var kind, projectID, ref string
		if err := rows.Scan(&kind, &projectID, &ref); err != nil {
			return fmt.Errorf("backup: read retained file: %w", err)
		}
		rel, err := retainedFilePath(kind, projectID, ref)
		if err != nil {
			return &InvalidError{Detail: "invalid retained file reference: " + err.Error()}
		}
		if kind == "attachment" {
			if attachments[rel] != 1 {
				return &InvalidError{Detail: "retained attachment is missing or ambiguous: " + rel}
			}
			continue
		}
		source, ok := files[rel]
		if !ok || source.kind != fileKindRegular {
			return &InvalidError{Detail: "retained file is missing: " + rel}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := validateWorkerManifests(ctx, snapshot, files); err != nil {
		return err
	}
	return validateArtifactBodies(ctx, snapshot, files)
}

func retainedFilePath(kind, projectID, ref string) (string, error) {
	if (kind != "source" && kind != "baseline" && kind != "branch-meta" && projectID == "") ||
		strings.ContainsAny(projectID, `/\`) || projectID == "." || projectID == ".." {
		return "", fmt.Errorf("invalid project id")
	}
	var rel string
	switch kind {
	case "source":
		object, err := sourceblob.RelPath(ref)
		if err != nil {
			return "", err
		}
		rel = filepath.Join(enginepaths.SourceContentDirName, object)
	case "model":
		object, err := contentblob.RelPath(ref)
		if err != nil {
			return "", err
		}
		rel = filepath.Join("projects", projectID, object)
	case "artifact", "attachment":
		if _, err := sourceblob.RelPath(ref); err != nil {
			return "", err
		}
		dir := "artifacts"
		if kind == "attachment" {
			dir = "prompt-attachments"
		}
		rel = filepath.Join("projects", projectID, dir, ref)
	case "spill":
		if !filepath.IsLocal(ref) || strings.Contains(ref, "\\") {
			return "", fmt.Errorf("invalid spill path")
		}
		rel = filepath.Join("projects", projectID, ref)
	case "branch-meta":
		if !strings.HasPrefix(ref, enginepaths.WorkerBranchesDirName+"/") || !workerMetadataPath(ref, false) {
			return "", fmt.Errorf("invalid worker metadata reference")
		}
		rel = ref
	case "baseline":
		resolved, err := workspacebaseline.Path(string(filepath.Separator), ref)
		if err != nil {
			return "", err
		}
		rel = filepath.Join(enginepaths.WorkerBaselinesDirName, filepath.Base(resolved))
	default:
		return "", fmt.Errorf("unknown retained file kind %q", kind)
	}
	rel = filepath.ToSlash(rel)
	if !RestorableRelPath(rel) {
		return "", fmt.Errorf("retained file is outside backup scope")
	}
	return rel, nil
}

func validateStagedReferences(ctx context.Context, stagingDir string, manifest Manifest) error {
	if err := editoroutbox.Validate(ctx, stagingDir); err != nil {
		return err
	}
	snapshot, err := db.OpenReadOnly(ctx, filepath.Join(stagingDir, storeRelPath))
	if err != nil {
		return fmt.Errorf("backup: open captured store: %w", err)
	}
	defer func() { _ = snapshot.Close() }()
	if err := validateRequiredBranchTrees(ctx, snapshot, manifest); err != nil {
		return err
	}
	files := make(map[string]archiveSource, len(manifest.Files))
	for _, file := range manifest.Files {
		files[file.RelPath] = archiveSource{path: filepath.Join(stagingDir, filepath.FromSlash(file.RelPath)), kind: file.Kind}
	}
	return validateArchiveReferences(ctx, snapshot, files)
}

func validateWorkerManifests(ctx context.Context, snapshot db.DBTX, files map[string]archiveSource) error {
	rows, err := snapshot.QueryContext(ctx, `SELECT b.id,b.manifest_sha256,b.format_version FROM worker_baselines b WHERE EXISTS (SELECT 1 FROM worker_jobs j WHERE j.workspace_baseline_id=b.id OR j.workspace_overlay_id=b.id)`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, digest string
		var version int
		if err := rows.Scan(&id, &digest, &version); err != nil {
			return err
		}
		rel, err := retainedFilePath("baseline", "", id)
		if err != nil {
			return err
		}
		source, ok := files[rel]
		if !ok || source.kind != fileKindRegular || version != 1 || len(digest) != 64 {
			return &InvalidError{Detail: "worker manifest is missing, unsealed or unsupported: " + id}
		}
		actual, err := workspacebaseline.Digest(ctx, source.path)
		if err != nil {
			return err
		}
		if actual != digest {
			return &InvalidError{Detail: "worker manifest checksum differs: " + id}
		}
		reader, err := workspacebaseline.Open(ctx, source.path, workspacebaseline.ContentStore(source.path))
		if err != nil {
			return &InvalidError{Detail: "worker manifest cannot be read: " + id + ": " + err.Error()}
		}
		if err := reader.Close(); err != nil {
			return err
		}
	}
	return rows.Err()
}
