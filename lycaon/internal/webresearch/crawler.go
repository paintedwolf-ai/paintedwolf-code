package webresearch

import (
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/outboundhttp"
)

// Crawler shares host pacing and fetched indexes across research requests.
type Crawler struct {
	exchange Exchange
	interval time.Duration
	gatesMu  sync.Mutex
	gates    map[string]*hostGate
	robots   *ttlCache[string]
	indexes  *ttlCache[[]indexCandidate]
}

func NewCrawler(exchange Exchange) *Crawler {
	if exchange == nil {
		exchange = outboundhttp.Do
	}
	return &Crawler{
		exchange: exchange,
		interval: 500 * time.Millisecond,
		gates:    make(map[string]*hostGate),
		robots:   newTTLCache[string](siteIndexCacheTTL, politeMaxHosts),
		indexes:  newTTLCache[[]indexCandidate](siteIndexCacheTTL, siteIndexCacheMaxHosts),
	}
}
