package board

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"golang.org/x/sync/singleflight"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/standingpatterns"
)

type standingFlagCacheRow struct {
	epoch     repochange.Epoch
	rulesHash string
	flags     []standingpatterns.FlagCount
}

type standingFlagCache struct {
	mu    sync.Mutex
	rows  map[string]standingFlagCacheRow
	order []string
	load  func(context.Context, string, []standingpatterns.Pattern) []standingpatterns.FlagCount
	work  singleflight.Group
}

const standingFlagProjectCap = 32

func newStandingFlagCache(load func(context.Context, string, []standingpatterns.Pattern) []standingpatterns.FlagCount) *standingFlagCache {
	return &standingFlagCache{
		rows: make(map[string]standingFlagCacheRow),
		load: load,
	}
}

func (c *standingFlagCache) get(ctx context.Context, projectDir string, rules []standingpatterns.Pattern) []standingpatterns.FlagCount {
	epoch := repochange.CurrentEpoch(projectDir)
	rulesHash := standingRulesHash(rules)
	if flags, ok := c.lookup(projectDir, epoch, rulesHash); ok {
		return flags
	}
	key := fmt.Sprintf("%s\x00%s\x00%d\x00%s", projectDir, epoch.BootID, epoch.Value, rulesHash)
	for {
		if ctx.Err() != nil {
			return nil
		}
		pending := c.work.DoChan(key, func() (any, error) {
			if flags, ok := c.lookup(projectDir, epoch, rulesHash); ok {
				return flags, nil
			}
			flags := c.load(ctx, projectDir, rules)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if repochange.Coverage(projectDir).Complete && repochange.EpochCurrent(projectDir, epoch) {
				c.store(projectDir, standingFlagCacheRow{
					epoch: epoch, rulesHash: rulesHash, flags: append([]standingpatterns.FlagCount(nil), flags...),
				})
			}
			return flags, nil
		})
		select {
		case <-ctx.Done():
			return nil
		case result := <-pending:
			if result.Err != nil {
				continue
			}
			flags, _ := result.Val.([]standingpatterns.FlagCount)
			return append([]standingpatterns.FlagCount(nil), flags...)
		}
	}
}

func (c *standingFlagCache) lookup(projectDir string, epoch repochange.Epoch, rulesHash string) ([]standingpatterns.FlagCount, bool) {
	if !repochange.Coverage(projectDir).Complete || !repochange.EpochCurrent(projectDir, epoch) {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.rows[projectDir]
	if !ok || row.epoch != epoch || row.rulesHash != rulesHash {
		return nil, false
	}
	if !repochange.EpochCurrent(projectDir, epoch) {
		return nil, false
	}
	c.touchLocked(projectDir)
	return append([]standingpatterns.FlagCount(nil), row.flags...), true
}

func (c *standingFlagCache) store(projectDir string, row standingFlagCacheRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows[projectDir] = row
	c.touchLocked(projectDir)
	for len(c.order) > standingFlagProjectCap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.rows, oldest)
	}
}

func (c *standingFlagCache) touchLocked(projectDir string) {
	for index, existing := range c.order {
		if existing != projectDir {
			continue
		}
		c.order = append(c.order[:index], c.order[index+1:]...)
		break
	}
	c.order = append(c.order, projectDir)
}

func standingRulesHash(rules []standingpatterns.Pattern) string {
	raw, err := json.Marshal(rules)
	if err != nil {
		panic(fmt.Sprintf("marshal standing-pattern rules: %v", err))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

var processStandingFlags = newStandingFlagCache(standingpatterns.CountFlags)
