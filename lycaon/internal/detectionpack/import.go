package detectionpack

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Sentinel errors for ImportPack / RemoveDevicePack. API maps these to status codes.
var (
	ErrPackNotFound = errors.New("detection pack not found")
	// ErrPackNotRemovable covers every pack that arrived with an extension pack.
	// Removing one is an uninstall of its provider, not a delete here.
	ErrPackNotRemovable = errors.New("only an imported folder can be removed here")
	ErrPackIDCollision  = errors.New("detection pack id collides")
	ErrPackInvalid      = errors.New("detection pack invalid")
)

const (
	maxFileBytes = 64 * 1024
	maxPackBytes = 4 * 1024 * 1024
)

// ImportRequest validates and optionally copies a pack folder into the device catalog.
type ImportRequest struct {
	SourcePath string // absolute path to a folder containing pack.yaml
	DryRun     bool
	Replace    bool
}

// ImportResult is the preview/commit outcome of ImportPack.
type ImportResult struct {
	Pack          Pack
	RejectedRules []RejectedRule
	Ignored       []string
	// Rehearsal is what the folder's own fixtures.yaml claimed and did not do.
	// Reported, never blocking: the person importing decides whether a miss is
	// acceptable.
	Rehearsal []RehearsalFinding
}

// RejectedRule names a rule file that failed ParseRule.
type RejectedRule struct {
	File   string
	Reason string
}

// ImportPack validates a candidate pack folder and, unless DryRun, copies its
// allowlisted files into <configDir>/detection-packs/<validated id>/.
//
// contributed is the catalog's own packs, which the candidate is checked
// against: an id an extension pack already provides is refused here rather than
// imported into a permanent collision.
func ImportPack(configDir string, contributed []Pack, req ImportRequest) (ImportResult, error) {
	var out ImportResult
	if configDir == "" {
		return out, fmt.Errorf("%w: configDir required", ErrPackInvalid)
	}
	resolved, err := resolveImportSource(req.SourcePath)
	if err != nil {
		return out, err
	}
	man, err := readImportManifest(resolved)
	if err != nil {
		return out, err
	}
	if err := checkImportCollisions(configDir, contributed, man.ID, req.Replace); err != nil {
		return out, err
	}

	candidates, ignored, totalBytes, err := collectImportCandidates(resolved)
	if err != nil {
		return out, err
	}
	out.Ignored = ignored
	if totalBytes > maxPackBytes {
		return out, fmt.Errorf("%w: pack exceeds 4 MiB cap", ErrPackInvalid)
	}
	if ruleFileCount(candidates) > maxRulesPerPack {
		return out, fmt.Errorf("%w: exceeds %d rule files cap", ErrPackInvalid, maxRulesPerPack)
	}

	pack, rejected, err := buildImportPack(man, candidates)
	if err != nil {
		return out, err
	}
	pack, err = applyImportDuplicatePolicy(configDir, contributed, pack)
	if err != nil {
		return out, err
	}
	out.RejectedRules = rejected
	out.Pack = pack
	out.Rehearsal = rehearseImport(pack, resolved)
	if req.DryRun {
		return out, nil
	}
	if err := commitImport(configDir, man.ID, candidates, req.Replace); err != nil {
		return out, err
	}
	return out, nil
}

func applyImportDuplicatePolicy(configDir string, contributed []Pack, candidate Pack) (Pack, error) {
	current, err := LoadCatalog(Input{ConfigDir: configDir, Contributed: contributed})
	if err != nil {
		return Pack{}, err
	}
	packs := make([]Pack, 0, len(current.Packs)+1)
	for _, pack := range current.Packs {
		if pack.ID != candidate.ID {
			packs = append(packs, pack)
		}
	}
	packs = append(packs, candidate)
	sort.Slice(packs, func(i, j int) bool { return packs[i].ID < packs[j].ID })
	_ = markDuplicateRuleIDs(packs)
	for _, pack := range packs {
		if pack.ID == candidate.ID {
			return pack, nil
		}
	}
	return Pack{}, fmt.Errorf("%w: candidate pack missing after validation", ErrPackInvalid)
}

func resolveImportSource(sourcePath string) (string, error) {
	if !filepath.IsAbs(sourcePath) {
		return "", fmt.Errorf("%w: path must be absolute", ErrPackInvalid)
	}
	resolved, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return "", fmt.Errorf("%w: resolve path: %w", ErrPackInvalid, err)
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: source must be an existing directory", ErrPackInvalid)
	}
	return resolved, nil
}

func readImportManifest(resolved string) (packManifest, error) {
	var man packManifest
	manifestPath := filepath.Join(resolved, "pack.yaml")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return man, fmt.Errorf("%w: pack.yaml required: %w", ErrPackInvalid, err)
	}
	if manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() {
		return man, fmt.Errorf("%w: pack.yaml must be a regular file, not a symlink", ErrPackInvalid)
	}
	manData, err := os.ReadFile(manifestPath) // #nosec G304
	if err != nil {
		return man, fmt.Errorf("%w: pack.yaml required: %w", ErrPackInvalid, err)
	}
	if int64(len(manData)) > maxFileBytes {
		return man, fmt.Errorf("%w: file exceeds 64 KiB cap: pack.yaml", ErrPackInvalid)
	}
	if err := yaml.Unmarshal(manData, &man); err != nil {
		return man, fmt.Errorf("%w: parse pack.yaml: %w", ErrPackInvalid, err)
	}
	if !packIDPattern.MatchString(man.ID) {
		return man, fmt.Errorf("%w: id must match %s", ErrPackInvalid, packIDPattern.String())
	}
	if strings.TrimSpace(man.Label) == "" || strings.TrimSpace(man.Description) == "" {
		return man, fmt.Errorf("%w: label and description required", ErrPackInvalid)
	}
	return man, nil
}

func checkImportCollisions(configDir string, contributed []Pack, id string, replace bool) error {
	for _, p := range contributed {
		if p.ID != id {
			continue
		}
		if p.ProviderPackID != "" {
			return fmt.Errorf("%w: extension pack %s already provides %q", ErrPackIDCollision, p.ProviderPackID, id)
		}
		return fmt.Errorf("%w: %q is already provided", ErrPackIDCollision, id)
	}
	destDir := filepath.Join(DevicePacksDir(configDir), id)
	if _, err := os.Stat(destDir); err == nil {
		if !replace {
			return fmt.Errorf("%w: device pack %q already exists", ErrPackIDCollision, id)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%w: stat destination: %w", ErrPackInvalid, err)
	}
	return nil
}

func ruleFileCount(candidates []importCandidate) int {
	n := 0
	for _, c := range candidates {
		if _, ok := RuleRelSlug(c.rel); ok {
			n++
		}
	}
	return n
}

func buildImportPack(man packManifest, candidates []importCandidate) (Pack, []RejectedRule, error) {
	equivalents, _ := sanitizeEquivalents(man.Equivalents)
	pack := Pack{
		ID:          man.ID,
		Label:       man.Label,
		Description: man.Description,
		Source:      SourceDevice,
		Enabled:     true,
		Equivalents: equivalents,
	}
	var rejected []RejectedRule
	var rules []Rule
	for _, c := range candidates {
		slug, isRule := RuleRelSlug(c.rel)
		if !isRule {
			continue
		}
		body, err := os.ReadFile(c.abs) // #nosec G304
		if err != nil {
			rejected = append(rejected, RejectedRule{File: c.rel, Reason: err.Error()})
			continue
		}
		rule, err := ParseRule(body)
		if err != nil {
			rejected = append(rejected, RejectedRule{File: c.rel, Reason: err.Error()})
			continue
		}
		rule.Slug = slug
		rules = append(rules, rule)
	}
	if len(rules) == 0 {
		return Pack{}, rejected, fmt.Errorf("%w: zero parsable rules", ErrPackInvalid)
	}
	pack.Rules = rules
	return pack, rejected, nil
}

// rehearseImport runs the candidate's own fixtures before anything is copied,
// so the preview can say what the rules did rather than only what they claim.
// A folder with no fixtures rehearses nothing and reports nothing.
func rehearseImport(pack Pack, sourceDir string) []RehearsalFinding {
	data, err := os.ReadFile(filepath.Join(sourceDir, PackFixturesFile)) // #nosec G304 -- user-chosen import folder
	if err != nil {
		return nil
	}
	corpus, err := ParseFixtures(data)
	if err != nil {
		return []RehearsalFinding{{PackID: pack.ID, Kind: RehearsalUnknownRule, Rule: PackFixturesFile}}
	}
	semantics, err := LoadActionSemantics("")
	if err != nil {
		return nil
	}
	return Rehearse(pack, corpus, semantics)
}

func commitImport(configDir, packID string, candidates []importCandidate, replace bool) error {
	deviceRoot, err := ensureDevicePacksDir(configDir)
	if err != nil {
		return err
	}
	resolvedDeviceRoot, err := filepath.EvalSymlinks(deviceRoot)
	if err != nil {
		return fmt.Errorf("%w: resolve device packs dir: %w", ErrPackInvalid, err)
	}
	if !containedPath(resolvedDeviceRoot, filepath.Join(resolvedDeviceRoot, packID)) {
		return fmt.Errorf("%w: destination escapes device packs directory", ErrPackInvalid)
	}

	stageDir := filepath.Join(deviceRoot, ".import-"+randomHex(8))
	if err := os.Mkdir(stageDir, 0o700); err != nil {
		return fmt.Errorf("%w: stage: %w", ErrPackInvalid, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stageDir)
		}
	}()

	for _, c := range candidates {
		dst := filepath.Join(stageDir, c.rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fmt.Errorf("%w: stage mkdir: %w", ErrPackInvalid, err)
		}
		if err := copyFileFn(c.abs, dst); err != nil {
			return fmt.Errorf("%w: copy %s: %w", ErrPackInvalid, c.rel, err)
		}
	}

	finalDir := filepath.Join(deviceRoot, packID)
	var tmpReplace string
	if replace {
		if _, err := os.Stat(finalDir); err == nil {
			tmpReplace = finalDir + ".replaced-" + randomHex(4)
			if err := os.Rename(finalDir, tmpReplace); err != nil {
				return fmt.Errorf("%w: replace backup: %w", ErrPackInvalid, err)
			}
		}
	}
	if err := os.Rename(stageDir, finalDir); err != nil {
		if tmpReplace != "" {
			_ = os.Rename(tmpReplace, finalDir)
		}
		return fmt.Errorf("%w: commit rename: %w", ErrPackInvalid, err)
	}
	cleanup = false
	if tmpReplace != "" {
		_ = os.RemoveAll(tmpReplace)
	}
	return nil
}

// RemoveDevicePack deletes a device pack directory. A pack an extension
// contributed is refused: it is removed by uninstalling or disabling its
// provider, and a delete here would be undone at the next resolve.
func RemoveDevicePack(configDir string, contributed []Pack, packID string) error {
	if !packIDPattern.MatchString(packID) {
		return fmt.Errorf("%w: invalid pack id", ErrPackInvalid)
	}
	for _, p := range contributed {
		if p.ID == packID {
			return ErrPackNotRemovable
		}
	}
	deviceRoot := DevicePacksDir(configDir)
	resolvedRoot, err := filepath.EvalSymlinks(deviceRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrPackNotFound
		}
		return fmt.Errorf("%w: resolve device packs: %w", ErrPackInvalid, err)
	}
	target := filepath.Join(deviceRoot, packID)
	targetInfo, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrPackNotFound
		}
		return fmt.Errorf("%w: inspect pack: %w", ErrPackInvalid, err)
	}
	if targetInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: pack path must not be a symlink", ErrPackInvalid)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrPackNotFound
		}
		return fmt.Errorf("%w: resolve pack: %w", ErrPackInvalid, err)
	}
	if !containedPath(resolvedRoot, resolvedTarget) {
		return fmt.Errorf("%w: pack path escapes device packs directory", ErrPackInvalid)
	}
	fi, err := os.Stat(resolvedTarget)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrPackNotFound
		}
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: pack path is not a directory", ErrPackInvalid)
	}
	return os.RemoveAll(target)
}

type importCandidate struct {
	rel  string
	abs  string
	size int64
}

func collectImportCandidates(root string) ([]importCandidate, []string, int64, error) {
	var candidates []importCandidate
	var ignored []string
	var total int64

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "rules" {
				return nil
			}
			ignored = append(ignored, rel+"/")
			return filepath.SkipDir
		}

		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			ignored = append(ignored, rel)
			return nil
		}
		if !allowlistedImportPath(rel) {
			ignored = append(ignored, rel)
			return nil
		}
		if info.Size() > maxFileBytes {
			return fmt.Errorf("%w: file exceeds 64 KiB cap: %s", ErrPackInvalid, rel)
		}
		total += info.Size()
		candidates = append(candidates, importCandidate{rel: rel, abs: path, size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, ignored, total, err
	}
	return candidates, ignored, total, nil
}

func allowlistedImportPath(rel string) bool {
	rel = filepath.ToSlash(rel)
	switch rel {
	case PackManifestFile, PackFixturesFile:
		return true
	}
	_, isRule := RuleRelSlug(rel)
	return isRule
}

func ensureDevicePacksDir(configDir string) (string, error) {
	dir := DevicePacksDir(configDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("%w: mkdir device packs: %w", ErrPackInvalid, err)
	}
	return dir, nil
}

// copyFileFn is the file copy used during import commit; tests may override it.
var copyFileFn = copyFile

func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}

func containedPath(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == target {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(target, root+sep)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte("fallback00"))[:n*2]
	}
	return hex.EncodeToString(b)
}
