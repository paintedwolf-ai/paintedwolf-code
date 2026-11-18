package sweeps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// sourceClientID is the editor and view client every source recipe acts as.
const sourceClientID = "window:conformance-source"

// sourceRecipes cover files, editor documents, source views, source history,
// search replacement, chat content and briefings.
var sourceRecipes = map[string]successRecipe{
	// Source views.
	"createSourceView":              {stage: stageCreate, prepare: sourceCreateViewInputs},
	"applySourceViewIntent":         {prepare: sourceApplyViewIntentInputs},
	"createSourcePresentation":      {prepare: sourceCreatePresentationInputs},
	"locateSourceView":              {prepare: sourceLocateViewInputs},
	"replaceSourceViewportInterest": {prepare: sourceViewportInterestInputs},
	"releaseSourceView":             {prepare: sourceReleaseViewInputs},

	// Editor documents.
	"createEditorDocumentSnapshot":   {prepare: sourceEditorSnapshotInputs},
	"replaceEditorDocument":          {prepare: sourceEditorReplaceInputs},
	"reloadEditorDocument":           {prepare: sourceEditorReloadInputs},
	"resolveEditorDocumentConflict":  {prepare: sourceEditorResolveInputs},
	"previewEditorSecretMark":        {prepare: sourceEditorPreviewSecretInputs},
	"markEditorSecret":               {prepare: sourceEditorMarkSecretInputs},
	"publishEditorDocumentPresence":  {prepare: sourceEditorPresenceInputs},
	"submitEditorDocumentUpdate":     {prepare: sourceEditorSubmitInputs},
	"replaceEditorDocumentRetention": {prepare: sourceEditorRetentionInputs},
	"revertEditorDocumentChange":     {prepare: sourceEditorRevertInputs},
	"listEditorDocumentChanges":      {seed: sourceSeedEditorChanges},

	// File mutations and their journal.
	"createProjectSourceEntry":  {prepare: sourceCreateEntryInputs},
	"copyProjectSource":         {prepare: sourceCopyInputs},
	"renameProjectSource":       {prepare: sourceRenameInputs},
	"deleteProjectSource":       {prepare: sourceDeleteInputs},
	"replaceProjectSource":      {prepare: sourceReplaceInputs},
	"makeProjectSourceEditable": {prepare: sourceMakeEditableInputs},
	"undoProjectSourceHistory":  {prepare: sourceUndoInputs},
	"redoProjectSourceHistory":  {prepare: sourceRedoInputs},
	"retrySourceOperation":      {prepare: sourceRetryInputs},
	"getProjectSourceRaw":       {prepare: sourceRawImageInputs},
	"applySearchReplacement":    {prepare: sourceApplyReplacementInputs},
	"searchProjectSource":       {seed: sourceSeedSearchFiles},

	// Recorded history.
	"listProjectSourceVersions":         {seed: sourceSeedVersions},
	"restoreProjectSourceVersion":       {prepare: sourceRestoreVersionInputs},
	"getProjectSourceComparison":        {prepare: sourceComparisonInputs},
	"digestSourceComparisons":           {prepare: sourceDigestInputs},
	"listProjectSourceWalk":             {seed: sourceSeedWalk},
	"completeProjectSourcePresentation": {prepare: sourceCompletePresentationInputs},
	"listProjectSourceSeen":             {seed: sourceSeedSeen},
	"withdrawProjectSourcePresentation": {prepare: sourceWithdrawPresentationInputs},

	// Repository history.
	"restoreProjectSourceCommitState": {prepare: sourceRestoreCommitInputs},
	"getProjectSourceRevisionReview":  {prepare: sourceRevisionReviewInputs},
	"getProjectSourceGitReview":       {prepare: sourceGitReviewInputs},

	// Briefings, chat content and host settings.
	"requestBriefing":          {prepare: sourceRequestBriefingInputs},
	"getBriefingRecord":        {prepare: sourceBriefingRecordInputs},
	"getChatContent":           {prepare: sourceChatContentInputs},
	"searchChatContent":        {prepare: sourceSearchChatContentInputs},
	"updateHostResource":       {prepare: sourceHostResourceInputs},
	"refreshPricingSource":     {prepare: sourcePricingRefreshInputs},
	"searchContributionSource": {prepare: sourceContributionSearchInputs},
}

// sourceState caches fixtures that are costly to build once per sweep.
type sourceState struct {
	names     int
	versions  *sourceVersioned
	git       *sourceGitFixture
	briefing  map[string]any
	recordID  string
	chatSHA   string
	chatQuery string
}

var sourceStates sync.Map

func sourceStateOf(s *conformanceSweep) *sourceState {
	state, _ := sourceStates.LoadOrStore(s, &sourceState{})
	return state.(*sourceState)
}

// sourceName is a fresh file name inside the fixture project.
func sourceName(s *conformanceSweep, tag, ext string) string {
	state := sourceStateOf(s)
	state.names++
	return fmt.Sprintf("conformance-%s-%d%s", tag, state.names, ext)
}

func sourceRootID(s *conformanceSweep) (string, error) {
	if s.host == nil || len(s.fixture.project.Roots) == 0 {
		return "", errors.New("the sweep has no production host and fixture root")
	}
	return s.fixture.project.Roots[0].ID, nil
}

// sourceWriteNew writes a fixture file only once, so a later prepare never
// rewrites what an earlier success changed.
func sourceWriteNew(s *conformanceSweep, name string, content []byte) error {
	path := filepath.Join(s.fixture.projectDir, filepath.FromSlash(name))
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, content, 0o644)
}

func sourceSHA(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func sourceDiskSHA(s *conformanceSweep, name string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(s.fixture.projectDir, filepath.FromSlash(name)))
	if err != nil {
		return "", err
	}
	return sourceSHA(raw), nil
}

// sourceSettle waits until the watcher has delivered every file change and
// detached work has drained.
func sourceSettle(ctx context.Context, s *conformanceSweep) error {
	if s.effects != nil {
		_, err := s.effects.mark(ctx)
		return err
	}
	if s.host != nil {
		s.host.Server.WaitForBackground(ctx)
	}
	return nil
}

func sourceString(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}

func sourceInt(m map[string]any, key string) int64 {
	value, _ := m[key].(float64)
	return int64(value)
}

func sourceObjects(m map[string]any, key string) []map[string]any {
	list, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

// sourceFindString returns the first string under key anywhere in value.
func sourceFindString(value any, key string) string {
	switch v := value.(type) {
	case map[string]any:
		if found, ok := v[key].(string); ok && found != "" {
			return found
		}
		for _, child := range v {
			if found := sourceFindString(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range v {
			if found := sourceFindString(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

// sourceFindObject returns the first object whose key holds want.
func sourceFindObject(value any, key, want string) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		if found, ok := v[key].(string); ok && found == want {
			return v
		}
		for _, child := range v {
			if found := sourceFindObject(child, key, want); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range v {
			if found := sourceFindObject(child, key, want); found != nil {
				return found
			}
		}
	}
	return nil
}

// sourceCreateEntry creates an empty file through the journaled create route
// and waits for it to land.
func sourceCreateEntry(ctx context.Context, s *conformanceSweep, name string) error {
	root, err := sourceRootID(s)
	if err != nil {
		return err
	}
	if _, err := s.prerequisite(ctx, "createProjectSourceEntry", successInputs{fields: map[string]any{
		"operation_id": uuid.NewString(), "path": name, "kind": "file", "root_id": root,
	}}); err != nil {
		return err
	}
	return sourceSettle(ctx, s)
}

// sourceRead reads a file through the source route: its file_id, sha256 and
// encoding.
func sourceRead(ctx context.Context, s *conformanceSweep, name string) (map[string]any, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return nil, err
	}
	return s.prerequisite(ctx, "getProjectSource", successInputs{query: map[string]string{"path": name, "root_id": root}})
}

func sourceWalk(ctx context.Context, s *conformanceSweep) (map[string]any, error) {
	return s.prerequisite(ctx, "listProjectSourceWalk", successInputs{query: map[string]string{"limit": "500"}})
}

// sourceWalkFile is the walk's file row for name.
func sourceWalkFile(ctx context.Context, s *conformanceSweep, name string) (map[string]any, error) {
	walk, err := sourceWalk(ctx, s)
	if err != nil {
		return nil, err
	}
	for _, file := range sourceObjects(walk, "files") {
		if sourceString(file, "path") == name {
			return file, nil
		}
	}
	return nil, fmt.Errorf("the walk lists no change to %s", name)
}

// --- Source views ---------------------------------------------------------

func sourceWorkspaceID(ctx context.Context, s *conformanceSweep) (string, error) {
	workspace, err := s.prerequisite(ctx, "getSourceWorkspace", successInputs{})
	if err != nil {
		return "", err
	}
	if id := sourceString(workspace, "workspace_id"); id != "" {
		return id, nil
	}
	return "", errors.New("the source workspace answered no workspace_id")
}

func sourceTreeViewFields(ctx context.Context, s *conformanceSweep) (map[string]any, error) {
	workspace, err := sourceWorkspaceID(ctx, s)
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation_id": uuid.NewString(), "client_id": sourceClientID, "workspace_id": workspace}, nil
}

// sourceCreateViewInputs names the project's own workspace. The two files
// give the view's search at least two rows to page.
func sourceCreateViewInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if _, err := sourceRootID(s); err != nil {
		return successInputs{}, err
	}
	for _, name := range []string{"conformance-view-a.txt", "conformance-view-b.txt"} {
		if err := sourceWriteNew(s, name, []byte("view\n")); err != nil {
			return successInputs{}, err
		}
	}
	if err := sourceSettle(ctx, s); err != nil {
		return successInputs{}, err
	}
	fields, err := sourceTreeViewFields(ctx, s)
	return successInputs{fields: fields}, err
}

// sourceViewRevision waits for the bound view to finish preparing and
// answers its intent revision.
func sourceViewRevision(ctx context.Context, s *conformanceSweep) (string, error) {
	if s.host == nil {
		return "", errors.New("the sweep has no production host")
	}
	s.host.Server.WaitForBackground(ctx)
	view, err := s.prerequisite(ctx, "getSourceView", successInputs{})
	if err != nil {
		return "", err
	}
	if state := sourceString(view, "state"); state != "ready" {
		return "", fmt.Errorf("the source view is %q after its preparation settled", state)
	}
	return sourceString(view, "intent_revision"), nil
}

func sourceApplyViewIntentInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	revision, err := sourceViewRevision(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{
		"kind": "tree", "operation_id": uuid.NewString(), "expected_intent_revision": revision,
		"command": map[string]any{"kind": "reveal", "address": map[string]any{"root_id": root, "path": "README.md"}},
	}}, nil
}

func sourceCreatePresentationInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	revision, err := sourceViewRevision(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{"operation_id": uuid.NewString(), "intent_revision": revision}}, nil
}

// sourceLocateViewInputs locates README.md by the tree anchor a row carries.
func sourceLocateViewInputs(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	anchor, err := json.Marshal(map[string]string{"root_id": root, "path": "README.md"})
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{query: map[string]string{"anchor": base64.RawURLEncoding.EncodeToString(anchor)}}, nil
}

func sourceViewportInterestInputs(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	presentation := s.values.byPrefix["/v1/projects/{id}/source/views/{view_id}/presentations/{presentation_id}"]
	if presentation == "" {
		return successInputs{}, errors.New("no source presentation was created")
	}
	return successInputs{fields: map[string]any{"presentation_id": presentation, "sequence": 1, "start": 0, "end": 1, "direction": 1}}, nil
}

// sourceReleaseViewInputs releases a view of its own, so the bound view
// outlives it for the presentation and interest releases.
func sourceReleaseViewInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	fields, err := sourceTreeViewFields(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	view, err := s.prerequisite(ctx, "createSourceView", successInputs{fields: fields})
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"view_id": sourceString(view, "id")}}, nil
}

// --- Editor documents -----------------------------------------------------

// sourceEditorDoc opens a document on a file of its own and answers it.
func sourceEditorDoc(ctx context.Context, s *conformanceSweep, name, content string) (map[string]any, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return nil, err
	}
	if err := sourceWriteNew(s, name, []byte(content)); err != nil {
		return nil, err
	}
	if err := sourceSettle(ctx, s); err != nil {
		return nil, err
	}
	doc, err := s.prerequisite(ctx, "openEditorDocument", successInputs{fields: map[string]any{
		"path": name, "root_id": root, "client_id": sourceClientID,
	}})
	if err != nil {
		return nil, err
	}
	if sourceString(doc, "id") == "" {
		return nil, fmt.Errorf("opening %s answered no document id", name)
	}
	return doc, nil
}

func sourceDocParams(doc map[string]any) map[string]string {
	return map[string]string{"document_id": sourceString(doc, "id")}
}

func sourceEditorSnapshotInputs(_ context.Context, _ *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	return successInputs{fields: map[string]any{"client_id": sourceClientID, "operation_id": uuid.NewString()}}, nil
}

// sourceEditorReplaceFields replaces a draft at its current revision; the
// nullable history vector and chat are sent as null.
func sourceEditorReplaceFields(doc map[string]any, content string) map[string]any {
	return map[string]any{
		"client_id": sourceClientID, "operation_id": uuid.NewString(), "expected_revision": sourceInt(doc, "revision"),
		"content": content, "eol": "lf", "mixed_eol": false, "history_vector": nil, "session_id": nil,
	}
}

func sourceEditorReplaceInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	doc, err := sourceEditorDoc(ctx, s, "conformance-editor-replace.txt", "before\n")
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: sourceDocParams(doc), fields: sourceEditorReplaceFields(doc, "after\n")}, nil
}

func sourceEditorCommandFields(doc map[string]any) map[string]any {
	return map[string]any{"client_id": sourceClientID, "operation_id": uuid.NewString(), "expected_revision": sourceInt(doc, "revision")}
}

func sourceEditorReloadInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	doc, err := sourceEditorDoc(ctx, s, "conformance-editor-reload.txt", "reload\n")
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: sourceDocParams(doc), fields: sourceEditorCommandFields(doc)}, nil
}

// sourceEditorResolveInputs accepts a merge against the bytes on disk.
func sourceEditorResolveInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	const name = "conformance-editor-resolve.txt"
	doc, err := sourceEditorDoc(ctx, s, name, "disk\n")
	if err != nil {
		return successInputs{}, err
	}
	disk, err := sourceDiskSHA(s, name)
	if err != nil {
		return successInputs{}, err
	}
	fields := sourceEditorCommandFields(doc)
	fields["disk_sha256"], fields["content"], fields["eol"] = disk, "resolved\n", "lf"
	return successInputs{params: sourceDocParams(doc), fields: fields}, nil
}

// sourceSecretValue is long enough to be marked and appears nowhere else.
const sourceSecretValue = "zq7-conformance-marked-value-4815"

func sourceSecretRange(ctx context.Context, s *conformanceSweep, name string) (map[string]any, map[string]any, error) {
	doc, err := sourceEditorDoc(ctx, s, name, sourceSecretValue+"\n")
	if err != nil {
		return nil, nil, err
	}
	return doc, map[string]any{"revision": sourceInt(doc, "revision"), "start": 0, "end": len(sourceSecretValue)}, nil
}

func sourceEditorPreviewSecretInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	doc, rng, err := sourceSecretRange(ctx, s, "conformance-editor-secret-preview.txt")
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: sourceDocParams(doc), fields: rng}, nil
}

func sourceEditorMarkSecretInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	doc, rng, err := sourceSecretRange(ctx, s, "conformance-editor-secret-mark.txt")
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: sourceDocParams(doc), fields: map[string]any{
		"range": rng, "name": "conformance-marked-secret", "purpose": "conformance", "operation_id": uuid.NewString(),
	}}, nil
}

// sourceEditorJoin joins a replica to doc and answers its frame.
func sourceEditorJoin(ctx context.Context, s *conformanceSweep, doc map[string]any, incarnation string) (map[string]any, error) {
	return s.prerequisite(ctx, "syncEditorDocument", successInputs{params: sourceDocParams(doc), fields: map[string]any{
		"client_id": sourceClientID, "incarnation": incarnation, "epoch": sourceInt(doc, "epoch"),
	}})
}

func sourceEditorPresenceInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	doc, err := sourceEditorDoc(ctx, s, "conformance-editor-presence.txt", "presence\n")
	if err != nil {
		return successInputs{}, err
	}
	incarnation := uuid.NewString()
	if _, err := sourceEditorJoin(ctx, s, doc, incarnation); err != nil {
		return successInputs{}, err
	}
	return successInputs{params: sourceDocParams(doc), fields: map[string]any{
		"client_id": sourceClientID, "incarnation": incarnation, "ranges": []any{}, "main": 0,
	}}, nil
}

// sourceEditorSubmitInputs submits a CRDT update authored by the replica the
// join registered: one string inserted into the document's text.
func sourceEditorSubmitInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	doc, err := sourceEditorDoc(ctx, s, "conformance-editor-update.txt", "update\n")
	if err != nil {
		return successInputs{}, err
	}
	frame, err := sourceEditorJoin(ctx, s, doc, uuid.NewString())
	if err != nil {
		return successInputs{}, err
	}
	replica := sourceInt(frame, "replica_id")
	if replica <= 0 || replica > 1<<32-1 {
		return successInputs{}, fmt.Errorf("the join answered replica_id %d", replica)
	}
	update := sourceCRDTInsert(uint32(replica), "inserted ")
	return successInputs{params: sourceDocParams(doc), fields: map[string]any{
		"client_id": sourceClientID, "replica_id": replica, "epoch": sourceInt(frame, "epoch"),
		"operation_id": uuid.NewString(), "update": base64.StdEncoding.EncodeToString(update),
	}}, nil
}

// sourceCRDTInsert encodes a v1 document update holding one string item at
// clock 0 with no neighbours, parented by the root type "text", and no
// deletions.
func sourceCRDTInsert(client uint32, text string) []byte {
	var b []byte
	b = sourceVarUint(b, 1) // clients
	b = sourceVarUint(b, 1) // structs for this client
	b = sourceVarUint(b, uint64(client))
	b = sourceVarUint(b, 0) // clock
	b = append(b, 4)        // string content, no origins
	b = sourceVarUint(b, 1) // parent named by a root key
	b = sourceVarString(b, "text")
	b = sourceVarString(b, text)
	return sourceVarUint(b, 0) // empty delete set
}

func sourceVarUint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func sourceVarString(b []byte, s string) []byte {
	return append(sourceVarUint(b, uint64(len(s))), s...)
}

// sourceEditorRetentionInputs retains for a window client that names itself.
func sourceEditorRetentionInputs(_ context.Context, _ *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	const client = "window:conformance-retention"
	return successInputs{fields: map[string]any{"client_id": client, "retained_clients": []any{client}, "document_ids": []any{}}}, nil
}

// sourceEditorRestoreChange makes one restore change on doc: a draft edit
// followed by a discard. It answers the document after the discard.
func sourceEditorRestoreChange(ctx context.Context, s *conformanceSweep, doc map[string]any, content string) (map[string]any, error) {
	replaced, err := s.prerequisite(ctx, "replaceEditorDocument", successInputs{params: sourceDocParams(doc), fields: sourceEditorReplaceFields(doc, content)})
	if err != nil {
		return nil, err
	}
	return s.prerequisite(ctx, "discardEditorDocument", successInputs{params: sourceDocParams(doc), fields: sourceEditorCommandFields(replaced)})
}

func sourceEditorRevertInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	doc, err := sourceEditorDoc(ctx, s, sourceName(s, "editor-revert", ".txt"), "base\n")
	if err != nil {
		return successInputs{}, err
	}
	discarded, err := sourceEditorRestoreChange(ctx, s, doc, "edited\n")
	if err != nil {
		return successInputs{}, err
	}
	changes, err := s.prerequisite(ctx, "listEditorDocumentChanges", successInputs{params: sourceDocParams(doc)})
	if err != nil {
		return successInputs{}, err
	}
	list := sourceObjects(changes, "changes")
	if len(list) == 0 {
		return successInputs{}, errors.New("a discard recorded no document change")
	}
	params := sourceDocParams(doc)
	params["change_id"] = sourceString(list[0], "operation_id")
	return successInputs{params: params, fields: map[string]any{
		"client_id": sourceClientID, "operation_id": uuid.NewString(), "epoch": sourceInt(discarded, "epoch"),
	}}, nil
}

// sourceSeedEditorChanges records two restore changes on the bound document.
func sourceSeedEditorChanges(ctx context.Context, s *conformanceSweep) error {
	id := s.values.byPrefix["/v1/projects/{id}/editor-documents/{document_id}"]
	if id == "" {
		return errors.New("no editor document is bound")
	}
	params := map[string]string{"document_id": id}
	doc, err := s.prerequisite(ctx, "createEditorDocumentSnapshot", successInputs{params: params, fields: map[string]any{"client_id": sourceClientID}})
	if err != nil {
		return err
	}
	for n := range 2 {
		if doc, err = sourceEditorRestoreChange(ctx, s, doc, fmt.Sprintf("seed %d\n", n)); err != nil {
			return err
		}
	}
	return nil
}

// --- File mutations -------------------------------------------------------

func sourceCreateEntryInputs(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{
		"operation_id": uuid.NewString(), "path": sourceName(s, "created", ".txt"), "kind": "file", "root_id": root,
	}}, nil
}

func sourceCopyInputs(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{
		"operation_id": uuid.NewString(), "root_id": root, "from": "README.md", "to": sourceName(s, "copy", ".md"),
	}}, nil
}

// sourceLandedFile writes a file and waits until the ledger has it.
func sourceLandedFile(ctx context.Context, s *conformanceSweep, tag string, content []byte) (string, error) {
	name := sourceName(s, tag, ".txt")
	if err := sourceWriteNew(s, name, content); err != nil {
		return "", err
	}
	return name, sourceSettle(ctx, s)
}

func sourceRenameInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	from, err := sourceLandedFile(ctx, s, "rename", []byte("rename\n"))
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{
		"operation_id": uuid.NewString(), "root_id": root, "from": from, "to": sourceName(s, "renamed", ".txt"),
	}}, nil
}

// sourceDeleteInputs deletes a file of its own; operation_id is a fresh key,
// never one bound from a listed file operation.
func sourceDeleteInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	name, err := sourceLandedFile(ctx, s, "delete", []byte("delete\n"))
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{query: map[string]string{"path": name, "root_id": root, "operation_id": uuid.NewString()}}, nil
}

func sourceReplaceInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	content := []byte("replace\n")
	name, err := sourceLandedFile(ctx, s, "replace", content)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{
		"operation_id": uuid.NewString(), "path": name, "root_id": root, "content": "replaced\n",
		"encoding": "utf-8", "base_sha256": sourceSHA(content),
	}}, nil
}

// sourceMakeEditableInputs makes a read-only file writable.
func sourceMakeEditableInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	content := []byte("read only\n")
	name, err := sourceLandedFile(ctx, s, "read-only", content)
	if err != nil {
		return successInputs{}, err
	}
	if err := os.Chmod(filepath.Join(s.fixture.projectDir, name), 0o444); err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{"path": name, "root_id": root, "base_sha256": sourceSHA(content)}}, nil
}

// sourceHistoryEntry answers the id of the history's next undo or redo.
func sourceHistoryEntry(ctx context.Context, s *conformanceSweep, direction string) (string, error) {
	history, err := s.prerequisite(ctx, "getProjectSourceHistory", successInputs{})
	if err != nil {
		return "", err
	}
	action, _ := history[direction].(map[string]any)
	if id := sourceString(action, "id"); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("source history offers no %s", direction)
}

func sourceHistoryFields(entry string) map[string]any {
	return map[string]any{"operation_id": uuid.NewString(), "expected_entry_id": entry}
}

func sourceUndoInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if err := sourceCreateEntry(ctx, s, sourceName(s, "undo", ".txt")); err != nil {
		return successInputs{}, err
	}
	entry, err := sourceHistoryEntry(ctx, s, "undo")
	return successInputs{fields: sourceHistoryFields(entry)}, err
}

func sourceRedoInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if err := sourceCreateEntry(ctx, s, sourceName(s, "redo", ".txt")); err != nil {
		return successInputs{}, err
	}
	entry, err := sourceHistoryEntry(ctx, s, "undo")
	if err != nil {
		return successInputs{}, err
	}
	if _, err := s.prerequisite(ctx, "undoProjectSourceHistory", successInputs{fields: sourceHistoryFields(entry)}); err != nil {
		return successInputs{}, err
	}
	if err := sourceSettle(ctx, s); err != nil {
		return successInputs{}, err
	}
	entry, err = sourceHistoryEntry(ctx, s, "redo")
	return successInputs{fields: sourceHistoryFields(entry)}, err
}

// sourceRetryInputs retries a file operation of its own once it finished.
func sourceRetryInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	operation := uuid.NewString()
	if _, err := s.prerequisite(ctx, "createProjectSourceEntry", successInputs{fields: map[string]any{
		"operation_id": operation, "path": sourceName(s, "retry", ".txt"), "kind": "file", "root_id": root,
	}}); err != nil {
		return successInputs{}, err
	}
	if err := sourceSettle(ctx, s); err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"operation_id": operation}}, nil
}

// sourceRawImageInputs reads a one-pixel PNG.
func sourceRawImageInputs(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return successInputs{}, err
	}
	const name = "conformance-image.png"
	if err := sourceWriteNew(s, name, encoded.Bytes()); err != nil {
		return successInputs{}, err
	}
	return successInputs{query: map[string]string{"path": name, "root_id": root}}, nil
}

// sourceReplaceToken appears only in the replacement fixture file.
const sourceReplaceToken = "conformancereplacetoken"

// sourceApplyReplacementInputs applies the first hunk the preview offers.
func sourceApplyReplacementInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	const name = "conformance-replace-token.txt"
	if _, err := sourceRootID(s); err != nil {
		return successInputs{}, err
	}
	if err := sourceWriteNew(s, name, []byte("before "+sourceReplaceToken+" after\n")); err != nil {
		return successInputs{}, err
	}
	if err := sourceSettle(ctx, s); err != nil {
		return successInputs{}, err
	}
	project := s.fixture.project.ID
	preview, err := s.prerequisite(ctx, "previewSearchReplacement", successInputs{fields: map[string]any{
		"query": sourceReplaceToken, "replacement": "conformancereplaced", "origin_project_id": project,
	}})
	if err != nil {
		return successInputs{}, err
	}
	for _, file := range sourceObjects(preview, "files") {
		if sourceString(file, "path") != name || len(sourceObjects(file, "hunks")) == 0 {
			continue
		}
		return successInputs{fields: map[string]any{
			"operation_id": uuid.NewString(), "query": sourceReplaceToken, "replacement": "conformancereplaced",
			"origin_project_id": project,
			"files": []any{map[string]any{
				"root_id": sourceString(file, "root_id"), "path": name, "sha256": sourceString(file, "sha256"), "hunks": []any{0},
			}},
		}}, nil
	}
	return successInputs{}, fmt.Errorf("the replacement preview offers no hunk in %s", name)
}

func sourceSeedSearchFiles(ctx context.Context, s *conformanceSweep) error {
	for range 2 {
		if _, err := sourceLandedFile(ctx, s, "search", []byte("search\n")); err != nil {
			return err
		}
	}
	return nil
}

// --- Recorded history -----------------------------------------------------

// sourceVersioned is one file with three recorded states.
type sourceVersioned struct {
	name     string
	fileID   string
	versions []map[string]any
}

func sourceVersionedFile(ctx context.Context, s *conformanceSweep) (*sourceVersioned, error) {
	state := sourceStateOf(s)
	if state.versions != nil {
		return state.versions, nil
	}
	root, err := sourceRootID(s)
	if err != nil {
		return nil, err
	}
	name := sourceName(s, "versions", ".txt")
	if err := sourceCreateEntry(ctx, s, name); err != nil {
		return nil, err
	}
	read, err := sourceRead(ctx, s, name)
	if err != nil {
		return nil, err
	}
	fileID, base, encoding := sourceString(read, "file_id"), sourceString(read, "sha256"), sourceString(read, "encoding")
	if fileID == "" || base == "" {
		return nil, fmt.Errorf("reading %s answered no file_id or sha256", name)
	}
	if encoding == "" {
		encoding = "utf-8"
	}
	for _, content := range []string{"version two\n", "version three\n"} {
		written, err := s.prerequisite(ctx, "replaceProjectSource", successInputs{fields: map[string]any{
			"operation_id": uuid.NewString(), "path": name, "root_id": root, "content": content,
			"encoding": encoding, "base_sha256": base,
		}})
		if err != nil {
			return nil, err
		}
		base = sourceString(written, "sha256")
	}
	if err := sourceSettle(ctx, s); err != nil {
		return nil, err
	}
	listed, err := s.prerequisite(ctx, "listProjectSourceVersions", successInputs{query: map[string]string{"file_id": fileID}})
	if err != nil {
		return nil, err
	}
	versions := sourceObjects(listed, "versions")
	if len(versions) < 2 {
		return nil, fmt.Errorf("%s holds %d recorded versions after two saves", name, len(versions))
	}
	state.versions = &sourceVersioned{name: name, fileID: fileID, versions: versions}
	return state.versions, nil
}

// sourceSeedVersions makes the listed file one with three recorded states.
func sourceSeedVersions(ctx context.Context, s *conformanceSweep) error {
	versioned, err := sourceVersionedFile(ctx, s)
	if err != nil {
		return err
	}
	s.values.bind("", "file_id", versioned.fileID)
	if bound := s.values.byName["file_id"]; bound != versioned.fileID {
		return fmt.Errorf("file_id is already bound to %s, which the seed cannot version", bound)
	}
	return nil
}

func sourceRestoreVersionInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	versioned, err := sourceVersionedFile(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	current, err := sourceDiskSHA(s, versioned.name)
	if err != nil {
		return successInputs{}, err
	}
	for _, version := range versioned.versions {
		if sourceString(version, "state") != "content" || sourceString(version, "content_sha256") == current {
			continue
		}
		return successInputs{
			params: map[string]string{"version_id": sourceString(version, "id")},
			fields: map[string]any{
				"operation_id": uuid.NewString(), "file_id": versioned.fileID, "root_id": root, "path": versioned.name,
				"base": map[string]any{"state": "content", "sha256": current},
			},
		}, nil
	}
	return successInputs{}, fmt.Errorf("%s has no earlier content version to restore", versioned.name)
}

func sourceComparisonInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	versioned, err := sourceVersionedFile(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{query: map[string]string{"version_id": sourceString(versioned.versions[0], "id")}}, nil
}

// sourceDigestInputs measures a recorded version; current sides are measured
// through a view, not a digest.
func sourceDigestInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	versioned, err := sourceVersionedFile(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{"sources": []any{map[string]any{
		"kind": "version", "version_id": sourceString(versioned.versions[0], "id"),
	}}}}, nil
}

func sourceSeedWalk(ctx context.Context, s *conformanceSweep) error {
	for range 2 {
		if err := sourceCreateEntry(ctx, s, sourceName(s, "walk", ".txt")); err != nil {
			return err
		}
	}
	return nil
}

// sourcePresentation is the walk's presentation of one new file.
func sourcePresentation(ctx context.Context, s *conformanceSweep, tag string) (map[string]any, error) {
	name := sourceName(s, tag, ".txt")
	if err := sourceCreateEntry(ctx, s, name); err != nil {
		return nil, err
	}
	file, err := sourceWalkFile(ctx, s, name)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{
		"file_id": sourceString(file, "file_id"), "effect_id": sourceString(file, "presentation_effect_id"),
		"ordinal": sourceInt(file, "presentation_ordinal"),
	}
	if fields["effect_id"] == "" {
		return nil, fmt.Errorf("the walk presents no effect for %s", name)
	}
	return fields, nil
}

func sourceCompletePresentationInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	fields, err := sourcePresentation(ctx, s, "seen")
	return successInputs{fields: fields}, err
}

func sourceSeedSeen(ctx context.Context, s *conformanceSweep) error {
	for range 2 {
		fields, err := sourcePresentation(ctx, s, "seen")
		if err != nil {
			return err
		}
		if _, err := s.prerequisite(ctx, "completeProjectSourcePresentation", successInputs{fields: fields}); err != nil {
			return err
		}
	}
	return nil
}

// sourceWithdrawPresentationInputs withdraws a look of its own through the
// ordinal it completed.
func sourceWithdrawPresentationInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	fields, err := sourcePresentation(ctx, s, "withdraw")
	if err != nil {
		return successInputs{}, err
	}
	if _, err := s.prerequisite(ctx, "completeProjectSourcePresentation", successInputs{fields: fields}); err != nil {
		return successInputs{}, err
	}
	return successInputs{
		params: map[string]string{"file_id": fields["file_id"].(string)},
		query:  map[string]string{"through_ordinal": strconv.FormatInt(fields["ordinal"].(int64), 10)},
	}, nil
}

// --- Repository history ---------------------------------------------------

// sourceGitFixture is a two-commit history in the fixture project: both
// files change in the second commit.
type sourceGitFixture struct {
	parent, head, blob string
	files              [2]string
}

func sourceGit(ctx context.Context, s *conformanceSweep, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = s.fixture.projectDir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Conformance", "GIT_AUTHOR_EMAIL=conformance@example.invalid",
		"GIT_COMMITTER_NAME=Conformance", "GIT_COMMITTER_EMAIL=conformance@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func sourceGitCommit(ctx context.Context, s *conformanceSweep, files [2]string, content, message string) (string, error) {
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(s.fixture.projectDir, name), []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	if _, err := sourceGit(ctx, s, "add", "--", files[0], files[1]); err != nil {
		return "", err
	}
	if _, err := sourceGit(ctx, s, "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	head, err := sourceGit(ctx, s, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	// The host seeds its ref baseline from the first observation and records
	// a movement on the next.
	return head, sourceSettle(ctx, s)
}

func sourceGitHistory(ctx context.Context, s *conformanceSweep) (*sourceGitFixture, error) {
	state := sourceStateOf(s)
	if state.git != nil {
		return state.git, nil
	}
	if _, err := sourceRootID(s); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(s.fixture.projectDir, ".git")); errors.Is(err, os.ErrNotExist) {
		if _, err := sourceGit(ctx, s, "init", "-q"); err != nil {
			return nil, err
		}
	}
	fixture := &sourceGitFixture{files: [2]string{"conformance-git-a.txt", "conformance-git-b.txt"}}
	var err error
	if fixture.parent, err = sourceGitCommit(ctx, s, fixture.files, "one\n", "conformance one"); err != nil {
		return nil, err
	}
	if fixture.blob, err = sourceGit(ctx, s, "rev-parse", fixture.parent+":"+fixture.files[0]); err != nil {
		return nil, err
	}
	if fixture.head, err = sourceGitCommit(ctx, s, fixture.files, "two\n", "conformance two"); err != nil {
		return nil, err
	}
	state.git = fixture
	return fixture, nil
}

// sourceRestoreCommitInputs restores the first commit's bytes over the
// second's.
func sourceRestoreCommitInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	history, err := sourceGitHistory(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	name := history.files[0]
	read, err := sourceRead(ctx, s, name)
	if err != nil {
		return successInputs{}, err
	}
	current, err := sourceDiskSHA(s, name)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{
		params: map[string]string{"commit_sha": history.parent},
		fields: map[string]any{
			"operation_id": uuid.NewString(), "file_id": sourceString(read, "file_id"), "root_id": root,
			"path": name, "source_path": name, "blob_oid": history.blob,
			"base": map[string]any{"state": "content", "sha256": current},
		},
	}, nil
}

func sourceRevisionReviewInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	root, err := sourceRootID(s)
	if err != nil {
		return successInputs{}, err
	}
	history, err := sourceGitHistory(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	// The page walk reads required inputs by name.
	s.values.bind("", "to_revision", history.head)
	return successInputs{query: map[string]string{"root_id": root, "from_revision": history.parent, "to_revision": history.head}}, nil
}

// sourceGitReviewInputs reviews the movement the second commit recorded.
func sourceGitReviewInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if _, err := sourceGitHistory(ctx, s); err != nil {
		return successInputs{}, err
	}
	walk, err := sourceWalk(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	for _, change := range sourceObjects(walk, "git_changes") {
		if id := sourceString(change, "id"); id != "" {
			s.values.bind("/v1/projects/{id}/source/git-changes/{git_change_id}", "", id)
			return successInputs{params: map[string]string{"git_change_id": id}}, nil
		}
	}
	return successInputs{}, errors.New("the walk lists no git movement after two commits")
}

// --- Briefings, chat content and host settings ---------------------------

// sourceWaitBriefings waits for every queued briefing generation.
func sourceWaitBriefings(ctx context.Context, s *conformanceSweep) error {
	briefings := s.host.Server.Sources.Briefings
	if briefings == nil || briefings.Engine == nil || briefings.Engine.Host() == nil {
		return errors.New("the host serves no briefing engine")
	}
	briefings.Engine.Host().Scheduler.Wait(ctx)
	return nil
}

// sourceBriefingTarget is the walk's briefing target for a new file's step,
// generated once so later requests are answered from storage.
func sourceBriefingTarget(ctx context.Context, s *conformanceSweep) (map[string]any, error) {
	state := sourceStateOf(s)
	if state.briefing != nil {
		return state.briefing, nil
	}
	if _, err := sourceRootID(s); err != nil {
		return nil, err
	}
	name := sourceName(s, "briefing", ".txt")
	if err := sourceCreateEntry(ctx, s, name); err != nil {
		return nil, err
	}
	walk, err := sourceWalk(ctx, s)
	if err != nil {
		return nil, err
	}
	targets, _ := walk["briefing_targets"].(map[string]any)
	var target map[string]any
	for _, file := range sourceObjects(walk, "files") {
		if sourceString(file, "path") == name {
			target, _ = targets[sourceString(file, "presentation_effect_id")].(map[string]any)
		}
	}
	if target == nil {
		return nil, fmt.Errorf("the walk names no briefing target for %s", name)
	}
	if _, err := s.prerequisite(ctx, "requestBriefing", successInputs{fields: sourceBriefingFields(target)}); err != nil {
		return nil, err
	}
	if err := sourceWaitBriefings(ctx, s); err != nil {
		return nil, err
	}
	state.briefing = target
	return target, nil
}

func sourceBriefingFields(target map[string]any) map[string]any {
	return map[string]any{"profile_id": "source_step", "target_ref": target, "trigger": "manual"}
}

func sourceRequestBriefingInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	target, err := sourceBriefingTarget(ctx, s)
	return successInputs{fields: sourceBriefingFields(target)}, err
}

func sourceBriefingRecordInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	state := sourceStateOf(s)
	if state.recordID == "" {
		target, err := sourceBriefingTarget(ctx, s)
		if err != nil {
			return successInputs{}, err
		}
		snapshot, err := s.prerequisite(ctx, "requestBriefing", successInputs{fields: sourceBriefingFields(target)})
		if err != nil {
			return successInputs{}, err
		}
		if state.recordID = sourceFindString(snapshot["result"], "record_id"); state.recordID == "" {
			return successInputs{}, fmt.Errorf("the generated briefing is %q with no evidence record", sourceString(snapshot, "status"))
		}
	}
	return successInputs{params: map[string]string{"record_id": state.recordID}}, nil
}

// sourceChatContent names the bound message's content revision as the
// transcript shows it, and a query it matches at least twice.
func sourceChatContent(ctx context.Context, s *conformanceSweep) (string, string, error) {
	state := sourceStateOf(s)
	if state.chatSHA != "" {
		return state.chatSHA, state.chatQuery, nil
	}
	messageID := s.values.byPrefix["/v1/sessions/{id}/messages/{message_id}"]
	if messageID == "" {
		return "", "", errors.New("no chat message is bound")
	}
	transcript, err := s.prerequisite(ctx, "listSessionMessages", successInputs{})
	if err != nil {
		return "", "", err
	}
	message := sourceFindObject(transcript, "id", messageID)
	if message == nil {
		return "", "", fmt.Errorf("the transcript does not show message %s", messageID)
	}
	content := sourceString(message, "content")
	counts := map[rune]int{}
	query := ""
	for _, r := range content {
		if counts[r]++; counts[r] >= 2 && (query == "" || counts[r] > counts[[]rune(query)[0]]) {
			query = string(r)
		}
	}
	if query == "" {
		query = "conformance"
	}
	state.chatSHA, state.chatQuery = sourceSHA([]byte(content)), query
	return state.chatSHA, state.chatQuery, nil
}

func sourceChatContentInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	sha, _, err := sourceChatContent(ctx, s)
	return successInputs{query: map[string]string{"field": "content", "sha256": sha}}, err
}

func sourceSearchChatContentInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	sha, query, err := sourceChatContent(ctx, s)
	return successInputs{query: map[string]string{"field": "content", "sha256": sha, "q": query}}, err
}

// sourceHostResourceInputs sets the bound resource to follow its default.
func sourceHostResourceInputs(_ context.Context, _ *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	return successInputs{fields: map[string]any{"access": "inherit"}}, nil
}

// sourcePricingRefreshInputs refreshes the default bundled pricing feed.
func sourcePricingRefreshInputs(_ context.Context, _ *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	return successInputs{params: map[string]string{"source_id": "models-dev"}}, nil
}

// sourceContributionSearchInputs searches the first source the captured
// contribution frame declares.
func sourceContributionSearchInputs(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	frame, err := s.prerequisite(ctx, "getContributions", successInputs{})
	if err != nil {
		return successInputs{}, err
	}
	sources := sourceObjects(frame, "search_sources")
	if len(sources) == 0 {
		return successInputs{}, errors.New("the contribution frame declares no search source")
	}
	return successInputs{
		params: map[string]string{"source_id": sourceString(sources[0], "id")},
		fields: map[string]any{"frame_revision": sourceString(frame, "frame_revision"), "query": "conformance"},
	}, nil
}
