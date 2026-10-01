package governance

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/textguard"
	"github.com/lycaon/lycaon/pkg/api"
)

// AgentsMDBodyTruncatedMarker is appended when an AGENTS.md body exceeds the inject cap.
const AgentsMDBodyTruncatedMarker = "\n…[agents_md_truncated]"

// DefaultAgentsMDInjectMaxBodyBytes matches prompt-budgets.yaml agents_md_inject.max_body_bytes.
const DefaultAgentsMDInjectMaxBodyBytes = 65536

// AgentsMDReadMaxBytes allows heading extraction beyond the smaller injection budget.
const AgentsMDReadMaxBytes = 4 << 20 // 4 MiB

var agentsMDPathTools = map[string]struct{}{
	"read":          {},
	"write":         {},
	"edit":          {},
	"replace_lines": {},
	"code_rewrite":  {},
	"grep":          {},
	"find":          {},
	"list_dir":      {},
	"stat":          {},
	"wc":            {},
	"jq":            {},
	"jq_edit":       {},
	"chmod":         {},
	"delete":        {},
	"mkdir":         {},
	"copy":          {},
	"move":          {},
}

// AgentsMDSessionState fixes one root and synchronizes access to its cached index.
type AgentsMDSessionState struct {
	mu       sync.Mutex
	Index    []ResolvedAgentsMD
	indexed  bool
	RootPath string
}

// Snapshot returns the cached index.
func (s *AgentsMDSessionState) Snapshot() []ResolvedAgentsMD {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Index
}

// SetIndex stores a freshly built index.
func (s *AgentsMDSessionState) SetIndex(index []ResolvedAgentsMD) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.Index = index
	s.indexed = true
	s.mu.Unlock()
}

// Indexed distinguishes a successful empty index from an unfinished walk.
func (s *AgentsMDSessionState) Indexed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.indexed || len(s.Index) > 0
}

// AgentsMDInject carries rendered policy and the files represented in it.
type AgentsMDInject struct {
	Content string
	Paths   []string
}

// BuildIndexInject renders the session-start AGENTS.md index block.
func BuildIndexInject(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string, index []ResolvedAgentsMD) (AgentsMDInject, error) {
	if renderer == nil {
		return AgentsMDInject{}, fmt.Errorf("inject renderer not configured")
	}
	if len(index) == 0 {
		return AgentsMDInject{}, nil
	}
	var paths []string
	items := make([]map[string]any, 0, len(index))
	for _, item := range index {
		path := strings.TrimSpace(item.Path)
		if path == "" {
			continue
		}
		paths = append(paths, path)
		items = append(items, map[string]any{
			"path": path,
		})
	}
	if len(items) == 0 {
		return AgentsMDInject{}, nil
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectAgentsMD, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, renderer, map[string]any{
		"mode":  "index",
		"index": items,
	})
	if err != nil {
		return AgentsMDInject{}, err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return AgentsMDInject{}, nil
	}
	return AgentsMDInject{Content: block, Paths: paths}, nil
}

// CapAgentsMDBody sanitizes and truncates oversized bodies to a heading digest or byte prefix.
func CapAgentsMDBody(content string, maxBytes int, path string) string {
	content = textguard.StripInvisibleFormatRunesUntilStable(content)
	if maxBytes <= 0 || len(content) <= maxBytes {
		return content
	}
	if digest, ok := degradeOversizedMarkdown(content, maxBytes, path); ok {
		return digest
	}
	return hardTruncateAgentsMDBody(content, maxBytes)
}

func hardTruncateAgentsMDBody(content string, maxBytes int) string {
	marker := AgentsMDBodyTruncatedMarker
	keep := maxBytes - len(marker)
	if keep < 0 {
		keep = 0
	}
	return truncateRuneSafe(content, keep) + marker
}

// BuildChainInject renders applicable AGENTS.md chain content for relPath under absRoot.
// maxBodyBytes caps each file body (UTF-8); use DefaultAgentsMDInjectMaxBodyBytes in production.
func BuildChainInject(
	ctx context.Context,
	renderer *prompts.InjectRenderer,
	sessionID string,
	absRoot string,
	relPath string,
	maxBodyBytes int,
) (AgentsMDInject, error) {
	if renderer == nil {
		return AgentsMDInject{}, fmt.Errorf("inject renderer not configured")
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return AgentsMDInject{}, nil
	}
	absRoot = strings.TrimSpace(absRoot)
	if absRoot == "" {
		return AgentsMDInject{}, nil
	}
	maxBodyBytes = normalizeAgentsMDMaxBodyBytes(maxBodyBytes)
	chain, err := ResolveChainLimited(absRoot, relPath, maxBodyBytes)
	if err != nil {
		return AgentsMDInject{}, err
	}
	if len(chain) == 0 {
		return AgentsMDInject{}, nil
	}
	var paths []string
	entries := make([]map[string]any, 0, len(chain))
	for _, item := range chain {
		path := strings.TrimSpace(item.Path)
		if path == "" {
			continue
		}
		content := item.Content
		if content == "" {
			continue
		}
		paths = append(paths, path)
		entries = append(entries, map[string]any{
			"path":    path,
			"content": CapAgentsMDBody(content, maxBodyBytes, path),
		})
	}
	if len(entries) == 0 {
		return AgentsMDInject{}, nil
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectAgentsMD, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, renderer, map[string]any{
		"mode":        "chain",
		"target_path": relPath,
		"chain":       entries,
	})
	if err != nil {
		return AgentsMDInject{}, err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return AgentsMDInject{}, nil
	}
	return AgentsMDInject{Content: block, Paths: paths}, nil
}

// ExtractPathScopedToolPaths returns repository paths from recent source-tool calls.
func ExtractPathScopedToolPaths(history []api.Message) []string {
	var paths []string
	seen := map[string]struct{}{}
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role != api.MessageRoleAssistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if _, ok := agentsMDPathTools[strings.TrimSpace(tc.Name)]; !ok {
				continue
			}
			for _, path := range toolCallScopePaths(tc) {
				path = strings.TrimSpace(path)
				if path == "" {
					continue
				}
				if _, dup := seen[path]; dup {
					continue
				}
				seen[path] = struct{}{}
				paths = append(paths, path)
			}
		}
	}
	return paths
}

func toolCallScopePaths(call api.ToolCall) []string {
	var paths []string
	appendPath := func(value any) {
		switch typed := value.(type) {
		case string:
			paths = append(paths, typed)
		case []any:
			for _, item := range typed {
				if path, ok := item.(string); ok {
					paths = append(paths, path)
				}
			}
		case []string:
			paths = append(paths, typed...)
		}
	}
	appendPath(call.Args["path"])
	appendPath(call.Args["paths"])
	for _, key := range []string{"copies", "moves"} {
		pairs, _ := call.Args[key].([]any)
		for _, item := range pairs {
			pair, _ := item.(map[string]any)
			appendPath(pair["from"])
			appendPath(pair["to"])
		}
	}
	return paths
}

// FirstConcreteScopePath picks the first non-glob scope or leg path suitable for chain resolution.
func FirstConcreteScopePath(paths []string) string {
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || p == "." {
			continue
		}
		if strings.ContainsAny(p, "*?[") {
			if base := scopePathBase(p); base != "" {
				return base
			}
			continue
		}
		return p
	}
	return "AGENTS.md"
}

func scopePathBase(p string) string {
	p = strings.TrimSpace(p)
	var keep []string
	for _, seg := range strings.Split(p, "/") {
		if strings.ContainsAny(seg, "*?[") {
			break
		}
		keep = append(keep, seg)
	}
	base := strings.Trim(strings.Join(keep, "/"), "/")
	if base == "" {
		return ""
	}
	return base
}
