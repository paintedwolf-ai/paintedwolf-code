package observability

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDebugLogCanCloseDuringConcurrentWrites(t *testing.T) {
	log, err := openJSONLDebugLogAt(filepath.Join(t.TempDir(), "events.jsonl"))
	testutil.FailErr(t, "open log", err)
	defer log.close()
	start := make(chan struct{})
	var writers sync.WaitGroup
	for range 8 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			for sequence := range 100 {
				log.write(map[string]int{"sequence": sequence})
			}
		}()
	}
	close(start)
	log.close()
	writers.Wait()
	log.write(map[string]bool{"after_close": true})
}
