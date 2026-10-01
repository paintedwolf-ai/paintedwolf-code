package settings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"gopkg.in/yaml.v3"
)

// VerifyConfig is the project's declared verify command. Empty Test is
// undeclared: bare verify() will not run; explicit checks remain valid evidence.
type VerifyConfig struct {
	Test string `yaml:"test,omitempty" json:"test,omitempty"`
}

// VerifyStore loads global and project verify overlays from <overlay>/verify.yaml.
// detectCache is the LLM proposal keyed by primary root; it is advisory and
// lives in the host config dir, not the project tree.
type VerifyStore struct {
	mu           sync.RWMutex
	global       *VerifyConfig
	projectCache map[string]VerifyConfig
	detectCache  map[string]VerifyDetectCandidate
	detectPath   string
}

// NewVerifyStore loads the optional global verify overlay and the detect-proposal cache.
func NewVerifyStore() (*VerifyStore, error) {
	path, err := userVerifyPath()
	if err != nil {
		return nil, err
	}
	detectPath, err := userVerifyDetectPath()
	if err != nil {
		return nil, err
	}
	return NewVerifyStoreAt(path, detectPath)
}

// NewVerifyStoreAt loads the global verify overlay at globalPath and keeps the
// detect-proposal cache at detectPath.
func NewVerifyStoreAt(globalPath, detectPath string) (*VerifyStore, error) {
	s := &VerifyStore{
		projectCache: make(map[string]VerifyConfig),
		detectCache:  loadVerifyDetectCache(detectPath),
		detectPath:   detectPath,
	}
	if cfg, err := loadVerifyFile(globalPath); err == nil {
		s.global = &cfg
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// Get returns the effective verify config for scope (project overlays global).
func (s *VerifyStore) Get(scope llm.SettingsScope, projectDir string) VerifyConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var cfg VerifyConfig
	if s.global != nil {
		cfg = *s.global
	}
	if scope == llm.SettingsScopeProject && projectDir != "" {
		if cached, ok := s.projectCache[projectDir]; ok {
			cfg = mergeVerifyConfig(cfg, cached)
		} else if proj, err := loadVerifyFile(projectVerifyPath(projectDir)); err == nil {
			cfg = mergeVerifyConfig(cfg, proj)
		}
	}
	return cfg
}

// VerifyTestCommand returns the project's declared test command, or "" if undeclared.
// Satisfies the session gate's VerifyConfigResolver.
func (s *VerifyStore) VerifyTestCommand(projectDir string) string {
	if s == nil {
		return ""
	}
	return s.Get(llm.SettingsScopeProject, projectDir).Test
}

// PutProject persists a project verify overlay at <overlay>/verify.yaml.
func (s *VerifyStore) PutProject(projectDir string, cfg VerifyConfig) error {
	cfg = normalizeVerifyConfig(cfg)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := writeSettingsFile(projectVerifyPath(projectDir), data); err != nil {
		return err
	}
	s.mu.Lock()
	s.projectCache[projectDir] = cfg
	s.mu.Unlock()
	return nil
}

func mergeVerifyConfig(base, overlay VerifyConfig) VerifyConfig {
	out := base
	if strings.TrimSpace(overlay.Test) != "" {
		out.Test = overlay.Test
	}
	return normalizeVerifyConfig(out)
}

func normalizeVerifyConfig(cfg VerifyConfig) VerifyConfig {
	cfg.Test = strings.TrimSpace(cfg.Test)
	return cfg
}

func loadVerifyFile(path string) (VerifyConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return VerifyConfig{}, err
	}
	var cfg VerifyConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return VerifyConfig{}, err
	}
	return normalizeVerifyConfig(cfg), nil
}

func userVerifyPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsoverlay.BasenameVerify), nil
}

func projectVerifyPath(projectDir string) string {
	return settingsoverlay.ProjectOverlayPath(projectDir, settingsoverlay.BasenameVerify)
}

// VerifyDetectCandidate is a proposal and its project-scoped presentation state.
type VerifyDetectCandidate struct {
	Command     string `json:"command"`
	Source      string `json:"source"`
	Dismissed   bool   `json:"dismissed,omitempty"`
	DismissedAt string `json:"dismissed_at,omitempty"`
}

// ProposalFor returns the cached detection result for a project root.
func (s *VerifyStore) ProposalFor(projectDir string) (VerifyDetectCandidate, bool) {
	if s == nil {
		return VerifyDetectCandidate{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	cand, ok := s.detectCache[projectDir]
	return cand, ok
}

// SetProposal persists a detection result, including an empty command.
func (s *VerifyStore) SetProposal(projectDir string, cand VerifyDetectCandidate) {
	if s == nil || strings.TrimSpace(projectDir) == "" {
		return
	}
	cand.Command = strings.TrimSpace(cand.Command)
	cand.Source = strings.TrimSpace(cand.Source)
	s.mu.Lock()
	if s.detectCache == nil {
		s.detectCache = make(map[string]VerifyDetectCandidate)
	}
	s.detectCache[projectDir] = cand
	snapshot := s.cloneDetectCacheLocked()
	s.mu.Unlock()
	persistVerifyDetectCache(s.detectPath, snapshot)
}

// ClearProposal drops a project's cached proposal.
func (s *VerifyStore) ClearProposal(projectDir string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if _, ok := s.detectCache[projectDir]; !ok {
		s.mu.Unlock()
		return
	}
	delete(s.detectCache, projectDir)
	snapshot := s.cloneDetectCacheLocked()
	s.mu.Unlock()
	persistVerifyDetectCache(s.detectPath, snapshot)
}

// DismissProposal hides a proposal while preserving its detection result.
func (s *VerifyStore) DismissProposal(projectDir, now string) {
	s.setDismissed(projectDir, true, now)
}

// UndismissProposal clears a project's dismissed flag so the suggestion can surface again.
func (s *VerifyStore) UndismissProposal(projectDir string) {
	s.setDismissed(projectDir, false, "")
}

func (s *VerifyStore) setDismissed(projectDir string, dismissed bool, now string) {
	if s == nil || strings.TrimSpace(projectDir) == "" {
		return
	}
	s.mu.Lock()
	if s.detectCache == nil {
		s.detectCache = make(map[string]VerifyDetectCandidate)
	}
	cand := s.detectCache[projectDir]
	cand.Dismissed = dismissed
	if dismissed {
		cand.DismissedAt = now
	} else {
		cand.DismissedAt = ""
	}
	s.detectCache[projectDir] = cand
	snapshot := s.cloneDetectCacheLocked()
	s.mu.Unlock()
	persistVerifyDetectCache(s.detectPath, snapshot)
}

func (s *VerifyStore) cloneDetectCacheLocked() map[string]VerifyDetectCandidate {
	out := make(map[string]VerifyDetectCandidate, len(s.detectCache))
	for k, v := range s.detectCache {
		out[k] = v
	}
	return out
}

const verifyDetectDocBytes = 16 * 1024

var verifyDetectDocNames = []string{"README.md", "AGENTS.md"}

// VerifyDetectOutcome is whether detection reached an answer about the project.
// Unavailable is transient; NoCommand and Found are cacheable.
type VerifyDetectOutcome int

const (
	// VerifyDetectUnavailable means detection did not produce a valid answer.
	VerifyDetectUnavailable VerifyDetectOutcome = iota
	// VerifyDetectNoCommand means the documents declare no verify command.
	VerifyDetectNoCommand
	// VerifyDetectFound means a command was extracted.
	VerifyDetectFound
)

// Ran reports whether detection reached an answer about the project.
func (o VerifyDetectOutcome) Ran() bool { return o != VerifyDetectUnavailable }

// DetectVerifyCommandLLM proposes a verify command from project documents.
func DetectVerifyCommandLLM(ctx context.Context, summarizer compaction.Summarizer, roots []string) (VerifyDetectCandidate, VerifyDetectOutcome) {
	if summarizer == nil {
		return VerifyDetectCandidate{}, VerifyDetectUnavailable
	}
	docs := readVerifyDetectDocs(roots)
	if strings.TrimSpace(docs) == "" {
		// Empty documentation is a completed detection result.
		return VerifyDetectCandidate{}, VerifyDetectNoCommand
	}
	system, err := guidance.RenderCatalog(ctx, guidance.UtilityVerifyDetectSystemRef, nil)
	if err != nil {
		return VerifyDetectCandidate{}, VerifyDetectUnavailable
	}
	reply, err := summarizer.Summarize(ctx, system, docs, 200)
	if err != nil {
		return VerifyDetectCandidate{}, VerifyDetectUnavailable
	}
	cand, ok := parseVerifyDetectReply(reply)
	switch {
	case !ok:
		return VerifyDetectCandidate{}, VerifyDetectUnavailable
	case strings.TrimSpace(cand.Command) == "":
		return VerifyDetectCandidate{}, VerifyDetectNoCommand
	default:
		return cand, VerifyDetectFound
	}
}

// readVerifyDetectDocs joins bounded project guidance without duplicate content.
func readVerifyDetectDocs(roots []string) string {
	var b strings.Builder
	seen := make(map[string]bool)
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		for _, name := range verifyDetectDocNames {
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}
			if len(content) > verifyDetectDocBytes {
				content = content[:verifyDetectDocBytes]
			}
			if seen[content] {
				continue
			}
			seen[content] = true
			b.WriteString("### ")
			b.WriteString(filepath.Join(filepath.Base(root), name))
			b.WriteString("\n")
			b.WriteString(content)
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

// parseVerifyDetectReply keeps a valid empty command distinct from malformed output.
func parseVerifyDetectReply(reply string) (VerifyDetectCandidate, bool) {
	start := strings.Index(reply, "{")
	end := strings.LastIndex(reply, "}")
	if start < 0 || end < start {
		return VerifyDetectCandidate{}, false
	}
	var parsed VerifyDetectCandidate
	if err := json.Unmarshal([]byte(reply[start:end+1]), &parsed); err != nil {
		return VerifyDetectCandidate{}, false
	}
	parsed.Command = strings.TrimSpace(parsed.Command)
	parsed.Source = strings.TrimSpace(parsed.Source)
	return parsed, true
}

func userVerifyDetectPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return enginepaths.VerifyDetectPathUnder(dir), nil
}

func loadVerifyDetectCache(path string) map[string]VerifyDetectCandidate {
	out := make(map[string]VerifyDetectCandidate)
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	if out == nil {
		out = make(map[string]VerifyDetectCandidate)
	}
	return out
}

func persistVerifyDetectCache(path string, cache map[string]VerifyDetectCandidate) {
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	_ = writeSettingsFile(path, data)
}
