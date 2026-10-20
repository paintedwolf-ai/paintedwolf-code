package commandprogram

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

const fixtureProtectedBytes = "fixture-protected-stream-value"

func streamFixtureBytes() []byte {
	return append(bytes.Repeat([]byte{'a', 0, '\r', '\n', 0xff}, 300000), []byte(fixtureProtectedBytes)...)
}

func TestScreenedPipeNativeAndProcessOutputRetainExactCompleteBytes(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprint("external=", external), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			raw := streamFixtureBytes()
			expected := bytes.ReplaceAll(raw, []byte(fixtureProtectedBytes), []byte(secretmatch.RedactedMarker))
			source := io.NopCloser(bytes.NewReader(raw))
			settle := func() error { return nil }
			if external {
				self, err := os.Executable()
				testutil.FailErr(t, "locate fixture process", err)
				cmd, cleanup, err := lyexec.PrepareCommand(ctx, self, []string{"-test.run=^TestScreenedPipeFixtureProcess$", "--", "fixture-byte-producer"}, lyexec.ExecOpts{Launch: lyexec.HostLaunch("stream fixture"), Dir: t.TempDir()})
				testutil.FailErr(t, "prepare fixture through existing executor", err)
				defer cleanup()
				source, err = cmd.StdoutPipe()
				testutil.FailErr(t, "open fixture stdout", err)
				testutil.FailErr(t, "start fixture", cmd.Start())
				waited := false
				defer func() {
					if !waited {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				}()
				settle = func() error { waited = true; return cmd.Wait() }
			}
			memory := &streamSharedMemoryFixture{maximum: 32 << 20}
			matcher := secretmatch.NewInertMatcher()
			matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
				return []secretmatch.HarvestedValue{{Secret: fixtureProtectedBytes, RuleID: secretmatch.ManagedRuleID, NonDisclosable: true}}
			})
			projector := captureprojection.New(matcher, nil)
			budget, err := NewByteBudget("fixture-output", 4<<20)
			testutil.FailErr(t, "admit output bytes", err)
			pipe, err := NewScreenedPipe(ctx, projector, captureprojection.Scope{}, "fixture-output", source, captureprojection.StreamOptions{WorkingMemory: memory, Bounds: secretmatch.StreamBounds{InputBytes: 4 << 20, OutputBytes: 4 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 40}}, budget)
			testutil.FailErr(t, "connect screening to byte pipe", err)
			defer pipe.Close()
			spill, err := tooloutput.RetainScreenedStream(ctx, tooloutput.ScreenedReader(pipe), tooloutput.StreamSpillOptions{Store: blobstore.Store{Root: t.TempDir()}, ClaimID: "fixture-stream", MaximumBytes: 4 << 20, PreviewBytes: 8, RetainedState: memory, WorkingMemory: memory, Admission: streamSpillAdmissionFixture{}})
			testutil.FailErr(t, "retain complete screened pipe", err)
			defer spill.Close()
			metadata, err := pipe.Wait(ctx)
			testutil.FailErr(t, "settle native screen", err)
			testutil.FailErr(t, "settle producing operation", settle())
			reader, err := spill.Open(ctx)
			testutil.FailErr(t, "open complete output", err)
			got, err := io.ReadAll(reader)
			testutil.FailErr(t, "read complete output", err)
			testutil.FailErr(t, "close output reader", reader.Close())
			rawHash, emittedHash := sha256.Sum256(raw), sha256.Sum256(expected)
			if !bytes.Equal(got, expected) || !spill.Complete || !metadata.Complete || !metadata.Redacted || len(spill.Preview) != 8 || metadata.RawSHA256 != hex.EncodeToString(rawHash[:]) || metadata.EmittedSHA256 != hex.EncodeToString(emittedHash[:]) || spill.EmittedSHA256 != metadata.EmittedSHA256 {
				t.Fatalf("screen/stream/spill diverged: screen=%+v stored=%d", metadata, len(got))
			}
			testutil.FailErr(t, "collect unreferenced output", spill.Close())
			memory.mu.Lock()
			defer memory.mu.Unlock()
			if memory.used != 0 || memory.peak > memory.maximum || memory.state != 0 || memory.statePeak == 0 {
				t.Fatalf("shared quota leaked/exceeded: used=%d peak=%d", memory.used, memory.peak)
			}
		})
	}
}

func TestScreenedPipeFixtureProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "fixture-byte-producer" {
		return
	}
	if _, err := os.Stdout.Write(streamFixtureBytes()); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

type streamSharedMemoryFixture struct {
	mu                                    sync.Mutex
	maximum, used, peak, state, statePeak int64
}

func (m *streamSharedMemoryFixture) ReserveBytes(ctx context.Context, n int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if n < 0 || n > m.maximum-m.used {
		return &StreamLimitError{Limit: "fixture-memory", Maximum: m.maximum, Observed: m.used + n}
	}
	m.used += n
	m.peak = max(m.peak, m.used)
	return nil
}
func (m *streamSharedMemoryFixture) ReleaseBytes(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.used -= n
}
func (m *streamSharedMemoryFixture) Reserve(ctx context.Context, n int64) (func(), error) {
	if err := m.ReserveBytes(ctx, n); err != nil {
		return nil, err
	}
	return func() { m.ReleaseBytes(n) }, nil
}

type streamSpillAdmissionFixture struct{}

func (streamSpillAdmissionFixture) ReserveSpill(context.Context, int64) (func(), error) {
	return func() {}, nil
}

func (m *streamSharedMemoryFixture) ReserveProgramState(ctx context.Context, n int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if n < 0 || n > (64<<20)-m.state {
		return nil, &StreamLimitError{Limit: "fixture-state", Maximum: 64 << 20, Observed: m.state + n}
	}
	m.state += n
	m.statePeak = max(m.statePeak, m.state)
	return func() { m.mu.Lock(); m.state -= n; m.mu.Unlock() }, nil
}
