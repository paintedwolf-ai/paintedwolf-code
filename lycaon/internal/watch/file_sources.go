package watch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/git"
)

const (
	MaxReconcileRoots = 8
	MaxReconcileFiles = 4096
	MaxReconcileBytes = 64 << 20
)

type manifestStore struct {
	mu      sync.Mutex
	entries map[string]map[string]string
}

func newManifestStore() *manifestStore {
	return &manifestStore{entries: make(map[string]map[string]string)}
}

func (m *manifestStore) key(watchID string, altIdx int) string {
	return fmt.Sprintf("%s:%d", watchID, altIdx)
}

func (m *manifestStore) set(watchID string, altIdx int, snap map[string]string) {
	if m == nil {
		return
	}
	cp := make(map[string]string, len(snap))
	for k, v := range snap {
		cp[k] = v
	}
	m.mu.Lock()
	m.entries[m.key(watchID, altIdx)] = cp
	m.mu.Unlock()
}

func (m *manifestStore) get(watchID string, altIdx int) (map[string]string, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src, ok := m.entries[m.key(watchID, altIdx)]
	if !ok {
		return nil, false
	}
	cp := make(map[string]string, len(src))
	for k, v := range src {
		cp[k] = v
	}
	return cp, true
}

func (m *manifestStore) removeWatch(watchID string) {
	if m == nil || watchID == "" {
		return
	}
	prefix := watchID + ":"
	m.mu.Lock()
	for k := range m.entries {
		if strings.HasPrefix(k, prefix) {
			delete(m.entries, k)
		}
	}
	m.mu.Unlock()
}

func fileLedgerOrdCursorKey() string {
	return "file:ledger_ord"
}

func fileManifestCursorKey(altIdx int) string {
	return fmt.Sprintf("file:manifest:%d", altIdx)
}

func initFileAndGitBaselines(ctx context.Context, s *Store, req *RegisterRequest) (map[int]map[string]string, *MatchInput, bool, error) {
	hasFile := false
	for _, c := range req.Conditions {
		if c.Kind == "file_change" {
			hasFile = true
		}
	}
	if hasFile {
		maxOrd, err := currentProjectLedgerOrdinal(s, ctx, req.Owner.ProjectID)
		if err != nil {
			return nil, nil, false, err
		}
		req.Cursors[fileLedgerOrdCursorKey()] = strconv.FormatInt(maxOrd, 10)
	}

	manifests := map[int]map[string]string{}
	rootCount := 0
	for altIdx, cond := range req.Conditions {
		switch cond.Kind {
		case "git_ref":
			grant, ok := resolveWatchRootGrant(req.Authority, cond.Root)
			if !ok || grant.Path == "" {
				continue
			}
			oid, exists, err := git.ResolveFullRef(grant.Path, cond.Ref)
			if err != nil {
				return nil, nil, false, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: fmt.Sprintf("conditions.%d.root", altIdx), Message: err.Error()}
			}
			req.Cursors[gitRefCursorKey(altIdx)] = formatGitRefState(oid, exists)
		case "file_change":
			rootCount++
			if rootCount > MaxReconcileRoots {
				gap := &MatchInput{
					SourceKind:   "file_change",
					OccurrenceID: fmt.Sprintf("file_gap:roots:%d", altIdx),
					Outcome:      "coverage_exceeded",
					Evidence:     map[string]any{"kind": "file_change", "root": cond.Root, "path": cond.Path, "gap": "root_budget_exceeded"},
				}
				return manifests, gap, true, nil
			}
			grant, ok := resolveWatchRootGrant(req.Authority, cond.Root)
			if !ok || grant.Path == "" {
				continue
			}
			snap, exceeded, gapReason, err := scanBoundedManifest(grant.Path, cond.Path, cond.Glob)
			if err != nil {
				return nil, nil, false, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: fmt.Sprintf("conditions.%d.path", altIdx), Message: err.Error()}
			}
			if exceeded {
				gap := &MatchInput{
					SourceKind:   "file_change",
					OccurrenceID: fmt.Sprintf("file_gap:budget:%d", altIdx),
					Outcome:      "coverage_exceeded",
					Evidence: map[string]any{
						"kind": "file_change",
						"root": cond.Root,
						"path": cond.Path,
						"gap":  gapReason,
					},
				}
				return manifests, gap, true, nil
			}
			manifests[altIdx] = snap
			req.Cursors[fileManifestCursorKey(altIdx)] = encodePersistedManifest(snap)
		}
	}
	return manifests, nil, false, nil
}

func encodePersistedManifest(snap map[string]string) string {
	if len(snap) <= 64 {
		raw, err := json.Marshal(snap)
		if err == nil && len(raw) <= 8192 {
			return string(raw)
		}
	}
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		_, _ = io.WriteString(h, k+"\x00"+snap[k]+"\n")
	}
	return "digest:" + hex.EncodeToString(h.Sum(nil))
}

func decodePersistedManifest(raw string) (snap map[string]string, digestOnly string) {
	raw = strings.TrimSpace(raw)
	if d, ok := strings.CutPrefix(raw, "digest:"); ok {
		return nil, d
	}
	if strings.HasPrefix(raw, "{") {
		var m map[string]string
		if json.Unmarshal([]byte(raw), &m) == nil {
			return m, ""
		}
	}
	return nil, ""
}

type ledgerEffectRow struct {
	EffectID   string
	Ordinal    int64
	RootID     string
	Path       string
	FromPath   string
	Op         string
	AfterSHA   string
	Origin     string
	Cause      string
	SessionID  string
	JobID      string
	ActorClass string
	IsOwn      bool
}

func reconcileFileChangeAlternative(s *Store, ctx context.Context, reg Registration, altIdx int, cond awaitstore.Condition) (bool, error) {
	grant, ok := resolveWatchRootGrant(reg.Authority, cond.Root)
	if !ok || grant.Path == "" {
		return true, s.MarkSourceLost(ctx, reg.WatchID)
	}
	if _, statErr := os.Stat(grant.Path); statErr != nil {
		return true, s.MarkSourceLost(ctx, reg.WatchID)
	}

	prevSnap, hasInMem := s.manifests.get(reg.WatchID, altIdx)
	var prevDigestOnly string
	if !hasInMem {
		prevSnap, prevDigestOnly = decodePersistedManifest(reg.Cursors[fileManifestCursorKey(altIdx)])
	}

	curSnap, exceeded, gapReason, err := scanBoundedManifest(grant.Path, cond.Path, cond.Glob)
	if err != nil {
		return true, s.MarkSourceLost(ctx, reg.WatchID)
	}
	if exceeded {
		_, _, _ = s.RecordMatch(ctx, reg.WatchID, MatchInput{
			SourceKind:   "file_change",
			OccurrenceID: fmt.Sprintf("file_gap:%d:%s", altIdx, gapReason),
			Outcome:      "coverage_exceeded",
			Evidence: map[string]any{
				"kind": "file_change",
				"root": cond.Root,
				"path": cond.Path,
				"gap":  gapReason,
			},
		}, nil)
		return true, s.MarkSourceLost(ctx, reg.WatchID)
	}

	lastOrd, _ := strconv.ParseInt(reg.Cursors[fileLedgerOrdCursorKey()], 10, 64)
	effects, maxOrd, err := queryNewLedgerEffects(s, ctx, reg.Owner, grant, lastOrd)
	if err != nil {
		return false, err
	}

	if prevSnap == nil && prevDigestOnly != "" {
		curEncoded := encodePersistedManifest(curSnap)
		s.manifests.set(reg.WatchID, altIdx, curSnap)
		if curEncoded != "digest:"+prevDigestOnly && len(effects) == 0 {
			_, recorded, rErr := s.RecordMatch(ctx, reg.WatchID, MatchInput{
				SourceKind:   "file_change",
				OccurrenceID: fmt.Sprintf("file_resync:%d:%s", altIdx, curEncoded),
				Outcome:      "resync",
				Evidence: map[string]any{
					"kind":        "file_change",
					"root":        cond.Root,
					"path":        cond.Path,
					"resync":      true,
					"actor_class": "unattributed",
				},
			}, map[string]string{
				fileManifestCursorKey(altIdx): curEncoded,
				fileLedgerOrdCursorKey():      strconv.FormatInt(maxOrd, 10),
			})
			return recorded, rErr
		}
	}
	if prevSnap == nil {
		s.manifests.set(reg.WatchID, altIdx, curSnap)
		_ = updateWatchCursor(s, ctx, reg.WatchID, fileManifestCursorKey(altIdx), encodePersistedManifest(curSnap))
		return false, nil
	}

	return recordFileChangeEffectsAndDiffs(s, ctx, fileChangeReconcileInput{
		reg:      reg,
		altIdx:   altIdx,
		cond:     cond,
		grant:    grant,
		prevSnap: prevSnap,
		curSnap:  curSnap,
		effects:  effects,
		lastOrd:  lastOrd,
		maxOrd:   maxOrd,
	})
}

type fileChangeReconcileInput struct {
	reg      Registration
	altIdx   int
	cond     awaitstore.Condition
	grant    WatchRootGrant
	prevSnap map[string]string
	curSnap  map[string]string
	effects  []ledgerEffectRow
	lastOrd  int64
	maxOrd   int64
}

func recordFileChangeEffectsAndDiffs(s *Store, ctx context.Context, in fileChangeReconcileInput) (bool, error) {
	coveredPaths := map[string]bool{}
	matchedAny := false
	curEncoded := encodePersistedManifest(in.curSnap)

	for _, eff := range in.effects {
		matchesPath := selectorMatchesRelPath(eff.Path, in.cond.Path, in.cond.Glob)
		matchesFrom := eff.FromPath != "" && selectorMatchesRelPath(eff.FromPath, in.cond.Path, in.cond.Glob)
		if !matchesPath && !matchesFrom {
			continue
		}
		coveredPaths[eff.Path] = true
		if eff.FromPath != "" {
			coveredPaths[eff.FromPath] = true
		}
		if !in.cond.IncludeOwn && eff.IsOwn {
			continue
		}
		ev := map[string]any{
			"kind":        "file_change",
			"root":        in.cond.Root,
			"path":        eff.Path,
			"op":          eff.Op,
			"effect_id":   eff.EffectID,
			"actor_class": eff.ActorClass,
		}
		if eff.FromPath != "" {
			ev["from_path"] = eff.FromPath
		}
		_, ok, rErr := s.RecordMatch(ctx, in.reg.WatchID, MatchInput{
			SourceKind:   "file_change",
			OccurrenceID: "file_effect:" + eff.EffectID,
			Outcome:      "satisfied",
			Evidence:     ev,
		}, map[string]string{
			fileLedgerOrdCursorKey():         strconv.FormatInt(eff.Ordinal, 10),
			fileManifestCursorKey(in.altIdx): curEncoded,
		})
		if rErr != nil {
			return matchedAny, rErr
		}
		if ok {
			matchedAny = true
			if !in.reg.Lifecycle.Repeat {
				s.manifests.set(in.reg.WatchID, in.altIdx, in.curSnap)
				return true, nil
			}
		}
	}

	diffs := diffManifests(in.prevSnap, in.curSnap)
	for _, d := range diffs {
		if coveredPaths[d.path] || (d.fromPath != "" && coveredPaths[d.fromPath]) {
			continue
		}
		effectID, actorClass, isOwn := lookupLatestPathEffect(s, ctx, in.reg.Owner, in.grant, d.path, d.afterDigest)
		if !in.cond.IncludeOwn && isOwn {
			continue
		}
		if effectID == "" {
			actorClass = "unattributed"
		}
		ev := map[string]any{
			"kind":        "file_change",
			"root":        in.cond.Root,
			"path":        d.path,
			"op":          d.op,
			"actor_class": actorClass,
		}
		if effectID != "" {
			ev["effect_id"] = effectID
		}
		if d.fromPath != "" {
			ev["from_path"] = d.fromPath
		}
		occ := fmt.Sprintf("file_disk:%d:%s:%s:%s->%s", in.altIdx, d.op, d.path, d.beforeDigest, d.afterDigest)
		_, ok, rErr := s.RecordMatch(ctx, in.reg.WatchID, MatchInput{
			SourceKind:   "file_change",
			OccurrenceID: occ,
			Outcome:      "satisfied",
			Evidence:     ev,
		}, map[string]string{
			fileLedgerOrdCursorKey():         strconv.FormatInt(in.maxOrd, 10),
			fileManifestCursorKey(in.altIdx): curEncoded,
		})
		if rErr != nil {
			return matchedAny, rErr
		}
		if ok {
			matchedAny = true
			if !in.reg.Lifecycle.Repeat {
				s.manifests.set(in.reg.WatchID, in.altIdx, in.curSnap)
				return true, nil
			}
		}
	}

	s.manifests.set(in.reg.WatchID, in.altIdx, in.curSnap)
	if in.maxOrd > in.lastOrd || len(diffs) > 0 {
		_ = updateWatchCursor(s, ctx, in.reg.WatchID, fileLedgerOrdCursorKey(), strconv.FormatInt(in.maxOrd, 10))
		_ = updateWatchCursor(s, ctx, in.reg.WatchID, fileManifestCursorKey(in.altIdx), curEncoded)
	}
	return matchedAny, nil
}

type manifestDiff struct {
	op           string
	path         string
	fromPath     string
	beforeDigest string
	afterDigest  string
}

func diffManifests(prev, cur map[string]string) []manifestDiff {
	var created, deleted, modified []string
	for p, curHash := range cur {
		prevHash, existed := prev[p]
		if !existed {
			created = append(created, p)
		} else if prevHash != curHash {
			modified = append(modified, p)
		}
	}
	for p := range prev {
		if _, exists := cur[p]; !exists {
			deleted = append(deleted, p)
		}
	}
	sort.Strings(created)
	sort.Strings(deleted)
	sort.Strings(modified)

	usedDeleted := map[string]bool{}
	var out []manifestDiff
	for _, cPath := range created {
		cHash := cur[cPath]
		matchedFrom := ""
		for _, dPath := range deleted {
			if !usedDeleted[dPath] && prev[dPath] == cHash && strings.HasPrefix(cHash, "sha256:") {
				matchedFrom = dPath
				usedDeleted[dPath] = true
				break
			}
		}
		if matchedFrom != "" {
			out = append(out, manifestDiff{op: "rename", path: cPath, fromPath: matchedFrom, beforeDigest: prev[matchedFrom], afterDigest: cHash})
		} else {
			out = append(out, manifestDiff{op: "create", path: cPath, afterDigest: cHash})
		}
	}
	for _, mPath := range modified {
		out = append(out, manifestDiff{op: "write", path: mPath, beforeDigest: prev[mPath], afterDigest: cur[mPath]})
	}
	for _, dPath := range deleted {
		if !usedDeleted[dPath] {
			out = append(out, manifestDiff{op: "delete", path: dPath, beforeDigest: prev[dPath], afterDigest: "absent"})
		}
	}
	return out
}

func currentProjectLedgerOrdinal(s *Store, ctx context.Context, projectID string) (int64, error) {
	if strings.TrimSpace(projectID) == "" {
		return 0, nil
	}
	var maxOrd sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(ordinal) FROM source_effects WHERE project_id = ?`, strings.TrimSpace(projectID)).Scan(&maxOrd)
	if err != nil {
		return 0, err
	}
	return maxOrd.Int64, nil
}

func queryNewLedgerEffects(s *Store, ctx context.Context, owner Owner, grant WatchRootGrant, afterOrdinal int64) ([]ledgerEffectRow, int64, error) {
	if strings.TrimSpace(owner.ProjectID) == "" {
		return nil, afterOrdinal, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.ordinal, e.root_id, e.path, e.from_path, e.op,
		COALESCE(v.content_sha256, ''), o.origin, o.cause, o.session_id, o.job_id
		FROM source_effects e
		JOIN source_operations o ON o.id = e.operation_id
		LEFT JOIN source_versions v ON v.id = e.after_version_id
		WHERE e.project_id = ? AND e.ordinal > ? AND o.branch_id = ?
		  AND (? = '' OR e.root_id = ? OR e.from_root_id = ?)
		ORDER BY e.ordinal ASC LIMIT 256`,
		owner.ProjectID, afterOrdinal, grant.BranchID,
		grant.RootID, grant.RootID, grant.RootID)
	if err != nil {
		return nil, afterOrdinal, err
	}
	defer func() { _ = rows.Close() }()
	var out []ledgerEffectRow
	maxOrd := afterOrdinal
	for rows.Next() {
		var r ledgerEffectRow
		if err := rows.Scan(&r.EffectID, &r.Ordinal, &r.RootID, &r.Path, &r.FromPath, &r.Op,
			&r.AfterSHA, &r.Origin, &r.Cause, &r.SessionID, &r.JobID); err != nil {
			return nil, maxOrd, err
		}
		if r.Ordinal > maxOrd {
			maxOrd = r.Ordinal
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, maxOrd, err
	}
	_ = rows.Close()
	for i := range out {
		out[i].ActorClass, out[i].IsOwn = classifyEffectRow(s, ctx, owner, out[i])
	}
	return out, maxOrd, nil
}

func lookupLatestPathEffect(s *Store, ctx context.Context, owner Owner, grant WatchRootGrant, relPath, afterDigest string) (string, string, bool) {
	if strings.TrimSpace(owner.ProjectID) == "" {
		return "", "", false
	}
	wantSHA, _ := strings.CutPrefix(afterDigest, "sha256:")
	var r ledgerEffectRow
	err := s.db.QueryRowContext(ctx, `SELECT e.id, e.ordinal, e.root_id, e.path, e.from_path, e.op,
		COALESCE(v.content_sha256, ''), o.origin, o.cause, o.session_id, o.job_id
		FROM source_effects e
		JOIN source_operations o ON o.id = e.operation_id
		LEFT JOIN source_versions v ON v.id = e.after_version_id
		WHERE e.project_id = ? AND o.branch_id = ? AND e.path = ?
		  AND (? = '' OR e.root_id = ?)
		ORDER BY e.ordinal DESC LIMIT 1`,
		owner.ProjectID, grant.BranchID, relPath, grant.RootID, grant.RootID).Scan(
		&r.EffectID, &r.Ordinal, &r.RootID, &r.Path, &r.FromPath, &r.Op,
		&r.AfterSHA, &r.Origin, &r.Cause, &r.SessionID, &r.JobID)
	if err != nil {
		return "", "", false
	}
	if wantSHA != "" && r.AfterSHA != "" && r.AfterSHA != wantSHA {
		return "", "", false
	}
	if afterDigest == "absent" && r.Op != "delete" {
		return "", "", false
	}
	actorClass, isOwn := classifyEffectRow(s, ctx, owner, r)
	return r.EffectID, actorClass, isOwn
}

type effectContribRow struct {
	origin    string
	sessionID string
	jobID     string
}

func queryEffectContribs(s *Store, ctx context.Context, effectID string) []effectContribRow {
	cRows, err := s.db.QueryContext(ctx, `SELECT o.origin, o.session_id, o.job_id
		FROM source_version_contributions c
		JOIN source_versions v ON v.id = c.author_version_id
		JOIN source_operations o ON o.id = v.operation_id
		JOIN source_effects e ON e.after_version_id = c.version_id
		WHERE e.id = ?`, effectID)
	if err != nil {
		return nil
	}
	defer func() { _ = cRows.Close() }()
	var contribs []effectContribRow
	for cRows.Next() {
		var c effectContribRow
		if cRows.Scan(&c.origin, &c.sessionID, &c.jobID) == nil {
			contribs = append(contribs, c)
		}
	}
	if cRows.Err() != nil {
		return nil
	}
	return contribs
}

func classifyEffectRow(s *Store, ctx context.Context, owner Owner, row ledgerEffectRow) (actorClass string, isOwn bool) {
	contribs := queryEffectContribs(s, ctx, row.EffectID)
	if len(contribs) == 0 {
		contribs = []effectContribRow{{origin: row.Origin, sessionID: row.SessionID, jobID: row.JobID}}
	}

	firstClass := singleContribActorClass(contribs[0], owner.SessionID)
	actorClass = firstClass
	allOwn := row.Cause != "overlay_promote"
	for _, c := range contribs {
		cls := singleContribActorClass(c, owner.SessionID)
		if cls != firstClass {
			actorClass = "mixed"
		}
		if c.origin != "agent" || strings.TrimSpace(c.sessionID) != owner.SessionID {
			allOwn = false
			continue
		}
		if owner.Kind == OwnerJob {
			if strings.TrimSpace(c.jobID) != owner.WorkerJobID {
				allOwn = false
			}
		} else if strings.TrimSpace(c.jobID) != "" {
			allOwn = false
		}
	}
	return actorClass, allOwn
}

func singleContribActorClass(c effectContribRow, viewerSessionID string) string {
	switch c.origin {
	case "agent":
		if strings.TrimSpace(c.sessionID) != "" && strings.TrimSpace(c.sessionID) == strings.TrimSpace(viewerSessionID) {
			return "you"
		}
		return "agent"
	case "user":
		return "user"
	case "external":
		return "external"
	default:
		return "unknown"
	}
}

func scanBoundedManifest(rootPath, selectorPath, globPattern string) (map[string]string, bool, string, error) {
	cleanRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, false, "", err
	}
	cleanRoot = filepath.Clean(cleanRoot)
	realRoot := cleanRoot
	if resolved, rErr := filepath.EvalSymlinks(cleanRoot); rErr == nil {
		realRoot = filepath.Clean(resolved)
	}

	selClean := path.Clean(strings.TrimSpace(selectorPath))
	targetAbs := cleanRoot
	if selClean != "." && selClean != "" {
		targetAbs = filepath.Join(cleanRoot, filepath.FromSlash(selClean))
	}
	if !isWithinDir(cleanRoot, targetAbs) {
		return nil, false, "", fmt.Errorf("selector escapes root")
	}

	manifest := make(map[string]string)
	info, err := os.Lstat(targetAbs)
	if os.IsNotExist(err) {
		return manifest, false, "", nil
	}
	if err != nil {
		return nil, false, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		dest, lErr := os.Readlink(targetAbs)
		if lErr != nil {
			return nil, false, "", lErr
		}
		resolvedTarget := dest
		if !filepath.IsAbs(resolvedTarget) {
			resolvedTarget = filepath.Join(filepath.Dir(targetAbs), resolvedTarget)
		}
		if evalDest, eErr := filepath.EvalSymlinks(resolvedTarget); eErr == nil && !isWithinDir(realRoot, evalDest) {
			return nil, false, "", fmt.Errorf("symlink selector points outside authorized root")
		}
		if !isWithinDir(cleanRoot, filepath.Clean(resolvedTarget)) && !isWithinDir(realRoot, filepath.Clean(resolvedTarget)) {
			return nil, false, "", fmt.Errorf("symlink selector points outside authorized root")
		}
		rel := relToRoot(cleanRoot, targetAbs)
		if selectorMatchesRelPath(rel, selClean, globPattern) {
			manifest[rel] = "link:" + dest
		}
		return manifest, false, "", nil
	}

	if !info.IsDir() {
		rel := relToRoot(cleanRoot, targetAbs)
		if !selectorMatchesRelPath(rel, selClean, globPattern) {
			return manifest, false, "", nil
		}
		if info.Size() > MaxReconcileBytes {
			return nil, true, "byte_budget_exceeded", nil
		}
		digest, hErr := hashRegularFile(targetAbs)
		if hErr == nil && digest != "" {
			manifest[rel] = digest
		}
		return manifest, false, "", nil
	}

	var totalBytes int64
	queue := []string{targetAbs}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		entries, rErr := os.ReadDir(dir)
		if rErr != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if name == ".git" {
				continue
			}
			full := filepath.Join(dir, name)
			rel := relToRoot(cleanRoot, full)
			linfo, lErr := os.Lstat(full)
			if lErr != nil {
				continue
			}
			if linfo.Mode()&os.ModeSymlink != 0 {
				dest, dErr := os.Readlink(full)
				if dErr != nil {
					continue
				}
				cand := dest
				if !filepath.IsAbs(cand) {
					cand = filepath.Join(dir, cand)
				}
				// Never traverse symlinks escaping the root; record in-root link targets safely.
				if !isWithinDir(cleanRoot, filepath.Clean(cand)) && !isWithinDir(realRoot, filepath.Clean(cand)) {
					continue
				}
				if selectorMatchesRelPath(rel, selClean, globPattern) {
					if len(manifest)+1 > MaxReconcileFiles {
						return nil, true, "file_budget_exceeded", nil
					}
					manifest[rel] = "link:" + dest
				}
				continue
			}
			if linfo.IsDir() {
				queue = append(queue, full)
				continue
			}
			if !linfo.Mode().IsRegular() || !selectorMatchesRelPath(rel, selClean, globPattern) {
				continue
			}
			if len(manifest)+1 > MaxReconcileFiles {
				return nil, true, "file_budget_exceeded", nil
			}
			if totalBytes+linfo.Size() > MaxReconcileBytes {
				return nil, true, "byte_budget_exceeded", nil
			}
			digest, hErr := hashRegularFile(full)
			if hErr != nil || digest == "" {
				continue
			}
			totalBytes += linfo.Size()
			manifest[rel] = digest
		}
	}
	return manifest, false, "", nil
}

func hashRegularFile(fullPath string) (string, error) {
	f, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, MaxReconcileBytes+1)); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func isWithinDir(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	return candidate == root || strings.HasPrefix(candidate, root+string(os.PathSeparator))
}

func relToRoot(cleanRoot, fullPath string) string {
	rel, err := filepath.Rel(cleanRoot, fullPath)
	if err != nil {
		return filepath.ToSlash(fullPath)
	}
	return filepath.ToSlash(rel)
}

func selectorMatchesRelPath(relPath, selectorPath, globPattern string) bool {
	relPath = path.Clean(strings.TrimPrefix(filepath.ToSlash(relPath), "./"))
	sel := path.Clean(strings.TrimPrefix(filepath.ToSlash(selectorPath), "./"))
	if sel != "." && sel != "" {
		if relPath != sel && !strings.HasPrefix(relPath, sel+"/") {
			return false
		}
	}
	globPattern = strings.TrimSpace(globPattern)
	if globPattern == "" {
		return true
	}
	return matchWatchGlob(globPattern, relPath) || (sel != "." && sel != "" && strings.HasPrefix(relPath, sel+"/") && matchWatchGlob(globPattern, strings.TrimPrefix(relPath, sel+"/")))
}

func matchWatchGlob(pattern, candidate string) bool {
	pSegs := splitSlashSegments(path.Clean(pattern))
	cSegs := splitSlashSegments(path.Clean(candidate))
	return matchSegments(pSegs, cSegs)
}

func splitSlashSegments(s string) []string {
	if s == "" || s == "." {
		return nil
	}
	return strings.Split(s, "/")
}

func matchSegments(pSegs, cSegs []string) bool {
	if len(pSegs) == 0 {
		return len(cSegs) == 0
	}
	if pSegs[0] == "**" {
		if matchSegments(pSegs[1:], cSegs) {
			return true
		}
		return len(cSegs) > 0 && matchSegments(pSegs, cSegs[1:])
	}
	if len(cSegs) == 0 {
		return false
	}
	ok, err := path.Match(pSegs[0], cSegs[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pSegs[1:], cSegs[1:])
}
