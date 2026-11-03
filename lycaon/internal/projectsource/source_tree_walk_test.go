//go:build darwin || linux

package projectsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestBoundedSourceWalkKeepsFingerprintAndManifestBytes(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "tree")
	for _, name := range []string{"a", "a-", "é", "é/nested", "empty"} {
		testutil.FailErr(t, "seed directory", os.MkdirAll(filepath.Join(root, name), 0o750))
	}
	for i, name := range []string{"a/z", "a-/first", "a0", "é/nested/file", "é0", strings.Repeat("é", 120), strings.Repeat("🐺", 60)} {
		body := []byte{0, 255, 42}
		if i%3 == 1 {
			body = bytes.Repeat(body, 16384)
		}
		testutil.FailErr(t, "seed mixed-size binary", os.WriteFile(filepath.Join(root, name), body, 0o640))
	}
	testutil.FailErr(t, "seed link", os.Symlink("../a0", filepath.Join(root, "a", "link")))
	var expected []sourceledger.RecoveryEntry
	var framing bytes.Buffer
	testutil.FailErr(t, "record established lexical format", fs.WalkDir(os.DirFS(root), ".", func(rel string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		saved := sourceledger.RecoveryEntry{Path: rel, Mode: uint32(info.Mode())}
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				return err
			}
			saved.SHA = sourceblob.ContentSHA(content)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			saved.Link, err = os.Readlink(filepath.Join(root, rel))
			if err != nil {
				return err
			}
		}
		expected = append(expected, saved)
		return writeSourceTreeEntry(&framing, saved)
	}))
	want := sha256.Sum256(framing.Bytes())
	memory := testSourceOperationMemory(t)
	actual, _, err := sourceTreeFingerprint(t.Context(), root, memory)
	testutil.FailErr(t, "fingerprint bounded traversal", err)
	if actual != hex.EncodeToString(want[:]) {
		t.Fatalf("changed durable fingerprint: %s", actual)
	}
	service := NewSourceMutationService(nil, nil)
	plan := sourceMutationPlan{Kind: "delete", RootPath: parent, sourceMutationAttribution: sourceMutationAttribution{ProjectID: "project"}, sourceMutationRecovery: sourceMutationRecovery{RecoveryID: "recovery", TreeSHA: actual}}
	testutil.FailErr(t, "capture existing manifest", service.recovery.captureRecovery(t.Context(), &plan, root, memory))
	expectedBytes, err := json.Marshal(expected)
	testutil.FailErr(t, "encode expected manifest", err)
	actualBytes, err := json.Marshal(service.recovery.recoveryEntries["recovery"])
	testutil.FailErr(t, "encode captured manifest", err)
	if !bytes.Equal(actualBytes, expectedBytes) {
		t.Fatalf("manifest order or fields changed:\n%s\n%s", actualBytes, expectedBytes)
	}
	if use := memory.InUse(); use.Working != 0 || use.State != 0 {
		t.Fatalf("walk leaked memory: %+v", use)
	}
}

func TestSourceRemovalAdmitsNarrowWideAndDeepTraversalBeforeEffects(t *testing.T) {
	for _, shape := range []string{"narrow", "wide", "deep"} {
		t.Run(shape, func(t *testing.T) {
			service, project, root, _ := sourceMutationFixture(t)
			tree := filepath.Join(root, "tree")
			testutil.FailErr(t, "seed tree", os.Mkdir(tree, 0o700))
			count := 1
			if shape == "wide" {
				count = 2000
			}
			target := tree
			if shape == "deep" {
				for range 20 {
					target = filepath.Join(target, strings.Repeat("d", 20))
				}
				testutil.FailErr(t, "seed deep tree", os.MkdirAll(target, 0o700))
			}
			for i := range count {
				testutil.FailErr(t, "seed entry", os.WriteFile(filepath.Join(target, fmt.Sprintf("%s%04d", strings.Repeat("f", 200), i)), []byte("body"), 0o600))
			}
			memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 4 << 20, State: 600 << 10})
			testutil.FailErr(t, "bound traversal", err)
			removal := agentRemoval(t, project, "tree", "")
			removal.Recursive = true
			removal.Memory = memory
			if shape == "narrow" {
				_, err := service.InspectRemoval(t.Context(), fseffect.Location{Root: root, Rel: "tree"}, memory)
				testutil.FailErr(t, "inspect admitted tree", err)
				id, err := service.RemoveEntry(t.Context(), removal)
				testutil.FailErr(t, "remove admitted tree", err)
				_, err = service.Undo(t.Context(), "undo-bounded", project, SourceHistoryMutationRequest{ExpectedEntryID: id})
				testutil.FailErr(t, "restore admitted tree", err)
			} else {
				_, err := service.InspectRemoval(t.Context(), fseffect.Location{Root: root, Rel: "tree"}, memory)
				var limit *toolcontract.ProgramMemoryLimitError
				if !errors.As(err, &limit) || limit.Account != "state" {
					t.Fatalf("lost original memory refusal: %v", err)
				}
				var intents int
				testutil.FailErr(t, "count effect intents", service.Journal.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_mutations`).Scan(&intents))
				if intents != 0 {
					t.Fatalf("unadmitted traversal recorded effect intent: %d", intents)
				}
				id, err := service.RemoveEntry(t.Context(), removal)
				testutil.FailErr(t, "move aside without traversal", err)
				_, err = service.Undo(t.Context(), "undo-bounded", project, SourceHistoryMutationRequest{ExpectedEntryID: id})
				testutil.FailErr(t, "restore moved-aside tree without traversal", err)
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("tree changed or not restored: %v", err)
			}
			if use := memory.InUse(); use.Working != 0 || use.State != 0 {
				t.Fatalf("traversal leaked: %+v", use)
			}
		})
	}
}

func TestSourceWalkCancellationAndLinkCacheAdmissionRelease(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, "file"), []byte("body"), 0o600))
	testutil.FailErr(t, "seed hard link", os.Link(filepath.Join(root, "file"), filepath.Join(root, "link")))
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 64 << 10, State: 4 << 20})
	testutil.FailErr(t, "bound allocation cache", err)
	_, _, err = sourceTreeFingerprint(t.Context(), root, memory)
	var limit *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &limit) || limit.Account != "working" {
		t.Fatalf("unadmitted exact set: %v", err)
	}
	if use := memory.InUse(); use.Working != 0 || use.State != 0 {
		t.Fatalf("allocation admission leaked: %+v", use)
	}
	opened, err := os.OpenRoot(root)
	testutil.FailErr(t, "open traversal root", err)
	defer opened.Close()
	memory = testSourceOperationMemory(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	visited := 0
	err = walkSourceTree(ctx, opened, ".", memory, func(string, fs.FileInfo) error {
		visited++
		if visited == 2 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || visited != 2 {
		t.Fatalf("cancellation: visits=%d error=%v", visited, err)
	}
	if use := memory.InUse(); use.Working != 0 || use.State != 0 {
		t.Fatalf("cancelled traversal leaked: %+v", use)
	}
}
