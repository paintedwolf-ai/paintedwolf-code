package blobstore

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIdleStoreStateIsReleased(t *testing.T) {
	store := Store{Root: t.TempDir(), Dir: "prompt-attachments"}
	key := filepath.Clean(store.Root) + "\x00" + filepath.ToSlash(strings.Trim(store.Dir, "/"))
	_, release := store.acquireState()
	release()

	storeStates.Lock()
	_, retained := storeStates.byKey[key]
	storeStates.Unlock()
	if retained {
		t.Fatal("idle store state remained registered")
	}
}

func TestIdleRepairStatesStayBounded(t *testing.T) {
	root := t.TempDir()
	prefix := filepath.Clean(root) + "\x00"
	t.Cleanup(func() {
		storeStates.Lock()
		defer storeStates.Unlock()
		for key, state := range storeStates.byKey {
			if strings.HasPrefix(key, prefix) {
				state.contentPrune.close()
				state.stagingPrune.close()
				delete(storeStates.byKey, key)
			}
		}
	})
	for i := 0; i < maxIdleRepairStates*2; i++ {
		store := Store{Root: root, Dir: filepath.Join("attachments", strconv.Itoa(i))}
		state, release := store.acquireState()
		dir, err := os.Open(root)
		testutil.FailErr(t, "open repair directory", err)
		state.contentPrune.dir = dir
		release()
	}

	storeStates.Lock()
	idle := 0
	for key, state := range storeStates.byKey {
		if strings.HasPrefix(key, prefix) && state.users == 0 {
			idle++
		}
	}
	storeStates.Unlock()
	if idle > maxIdleRepairStates {
		t.Fatalf("idle repair states = %d, want <= %d", idle, maxIdleRepairStates)
	}
}
