package spawn

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/agentdef"
)

type implementDefaultSpawnYAML struct {
	AllowedAgents          []string          `yaml:"allowed_agents"`
	SurfaceLanes           map[string]string `yaml:"surface_lanes"`
	MaxInFlightTaskWorkers int               `yaml:"max_in_flight_task_workers"`
	MaxReadTaskWorkers     int               `yaml:"max_read_task_workers"`
	MaxWriteTaskWorkers    int               `yaml:"max_write_task_workers"`
}

var (
	spawnCfgOnce sync.Once
	spawnCfg     implementDefaultSpawnYAML
	spawnCfgErr  error

	// The task() worker caps below are read from implement-default-spawn.yaml.

	// MaxInFlightTaskWorkers is the host in-flight task() cap.
	MaxInFlightTaskWorkers int
	// DefaultMaxReadTaskWorkers is the default read-lane task() cap.
	DefaultMaxReadTaskWorkers int
	// DefaultMaxWriteTaskWorkers is the default write-lane task() cap.
	DefaultMaxWriteTaskWorkers int
)

func init() {
	cfg := bundledSpawnConfig()
	MaxInFlightTaskWorkers = cfg.MaxInFlightTaskWorkers
	DefaultMaxReadTaskWorkers = cfg.MaxReadTaskWorkers
	DefaultMaxWriteTaskWorkers = cfg.MaxWriteTaskWorkers
}

func loadImplementDefaultSpawn() (implementDefaultSpawnYAML, error) {
	data, err := config.Read(config.ImplementDefaultSpawn)
	if err != nil {
		return implementDefaultSpawnYAML{}, fmt.Errorf("read implement-default spawn: %w", err)
	}
	var raw implementDefaultSpawnYAML
	if err := config.DecodeYAML(data, &raw); err != nil {
		return implementDefaultSpawnYAML{}, fmt.Errorf("parse implement-default spawn: %w", err)
	}
	if len(raw.AllowedAgents) == 0 {
		return implementDefaultSpawnYAML{}, fmt.Errorf("implement-default spawn: allowed_agents is empty")
	}
	for surfaceID, lane := range raw.SurfaceLanes {
		if strings.TrimSpace(surfaceID) == "" {
			return implementDefaultSpawnYAML{}, fmt.Errorf("implement-default spawn: surface_lanes has an empty surface id")
		}
		if !slices.Contains(agentdef.DispatchLanes(), strings.TrimSpace(lane)) {
			return implementDefaultSpawnYAML{}, fmt.Errorf(
				"implement-default spawn: surface_lanes[%s] = %q is not one of %s",
				surfaceID, lane, strings.Join(agentdef.DispatchLanes(), ", "))
		}
	}
	if raw.MaxInFlightTaskWorkers <= 0 {
		return implementDefaultSpawnYAML{}, fmt.Errorf("implement-default spawn: max_in_flight_task_workers must be positive")
	}
	if raw.MaxReadTaskWorkers <= 0 {
		raw.MaxReadTaskWorkers = raw.MaxInFlightTaskWorkers
	}
	if raw.MaxWriteTaskWorkers <= 0 {
		raw.MaxWriteTaskWorkers = raw.MaxInFlightTaskWorkers
	}
	return raw, nil
}

func bundledSpawnConfig() implementDefaultSpawnYAML {
	spawnCfgOnce.Do(func() {
		spawnCfg, spawnCfgErr = loadImplementDefaultSpawn()
	})
	if spawnCfgErr != nil {
		panic(spawnCfgErr)
	}
	return spawnCfg
}

var (
	contributedMu     sync.RWMutex
	contributedAgents func() []string
)

// SetContributedAgentsSource installs the reader for worker agents installed
// packs contribute, so no caller needs a catalog to build a roster.
func SetContributedAgentsSource(fn func() []string) {
	contributedMu.Lock()
	defer contributedMu.Unlock()
	contributedAgents = fn
}

// AmbientAllowedAgents is the task() roster for a session with no workflow
// allowlist: the bundled agents from implement-default-spawn.yaml plus the
// worker agents installed packs contribute.
func AmbientAllowedAgents() []string {
	return WithContributedAgents(bundledSpawnConfig().AllowedAgents)
}

// WithContributedAgents adds the worker agents installed packs contribute to a
// declared roster, without duplicating anything already in it.
func WithContributedAgents(declared []string) []string {
	out := append([]string(nil), declared...)
	contributedMu.RLock()
	fn := contributedAgents
	contributedMu.RUnlock()
	if fn == nil {
		return out
	}
	seen := make(map[string]bool, len(out))
	for _, id := range out {
		seen[id] = true
	}
	extra := fn()
	sort.Strings(extra)
	for _, id := range extra {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
