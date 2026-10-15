package bialy_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/testutil"
)

// fakeEngine answers the protocol from a shell script: hello, one noul, one rank.
const fakeEngine = `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"hello"'*) printf '{"id":%s,"engine":{"name":"fake-bialy","model":"test","device":"cpu","head":"turn-load"}}\n' "$id" ;;
    *'"method":"decide"'*) printf '{"id":%s,"answers":{"edit":{"type":"noul","noul":0.91,"confidence":0.91},"kind":{"type":"choice","choice":"change","probabilities":{"change":0.8,"answer_only":0.2},"confidence":0.7}}}\n' "$id" ;;
    *'"method":"rank"'*) printf '{"id":%s,"scores":[0.1,3.7]}\n' "$id" ;;
    *) printf '{"id":%s,"error":"unknown method"}\n' "$id" ;;
  esac
done
`

func writeFakeEngine(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake engine")
	}
	path := filepath.Join(t.TempDir(), "bialy")
	testutil.FailErr(t, "write fake engine", os.WriteFile(path, []byte(fakeEngine), 0o755))
	return path
}

// The fake checkpoint contains every pinned path and its completion marker.
func writeFakeModel(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	layOutModel(t, dir)
	return dir
}

func layOutModel(t *testing.T, dir string) {
	t.Helper()
	for _, f := range bialy.ShippedModel.Files {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write model file", os.WriteFile(path, []byte("x"), 0o644))
	}
	testutil.FailErr(t, "write marker", os.WriteFile(filepath.Join(dir, bialy.CompleteMarker), []byte("test\n"), 0o644))
}

func fakeConfig(t *testing.T) bialy.Config {
	t.Helper()
	return bialy.Config{Binary: writeFakeEngine(t), ModelDir: writeFakeModel(t), ModelID: "test/model"}
}

func TestClientDecidesAndRanksOverTheLineProtocol(t *testing.T) {
	t.Parallel()
	client := bialy.New(fakeConfig(t))
	t.Cleanup(func() { _ = client.Close() })
	if !client.Available() {
		t.Fatal("client should resolve the fake engine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := client.Decide(ctx, decide.HeadTurnLoad, map[string]any{"user": "fix the typo"}, map[string]decide.Question{
		"edit": decide.Noul("needs edits?"),
		"kind": decide.Choice("kind?", map[string]string{"change": "", "answer_only": ""}),
	})
	testutil.FailErr(t, "Decide", err)
	if res.Answers["edit"].Noul != 0.91 || res.Answers["kind"].Choice != "change" {
		t.Fatalf("answers = %+v", res.Answers)
	}
	if res.Engine.Head != "turn-load" || res.Engine.Name != "fake-bialy" {
		t.Fatalf("engine = %+v", res.Engine)
	}
	scores, engine, err := client.Rank(ctx, decide.HeadCodeRank, "task", []string{"a", "b"})
	testutil.FailErr(t, "Rank", err)
	if len(scores) != 2 || scores[1] != 3.7 || engine.Model != "test" {
		t.Fatalf("scores = %v engine = %+v", scores, engine)
	}
}

func TestClientReportsMissingAnswers(t *testing.T) {
	t.Parallel()
	client := bialy.New(fakeConfig(t))
	t.Cleanup(func() { _ = client.Close() })
	_, err := client.Decide(context.Background(), decide.HeadTurnLoad, "s", map[string]decide.Question{"absent": decide.Noul("?")})
	if !errors.Is(err, decide.ErrEngine) {
		t.Fatalf("error = %v want ErrEngine", err)
	}
}

func TestClientDisabledAndUnresolvedAbstain(t *testing.T) {
	t.Parallel()
	disabled := bialy.New(bialy.Config{Binary: "/nonexistent/bialy", Disabled: true})
	if disabled.Available() {
		t.Fatal("disabled client must not be available")
	}
	if _, err := disabled.Decide(context.Background(), decide.HeadTurnLoad, "s", map[string]decide.Question{"q": decide.Noul("?")}); !errors.Is(err, decide.ErrDisabled) {
		t.Fatalf("disabled error = %v", err)
	}
	missing := bialy.New(bialy.Config{Binary: filepath.Join(t.TempDir(), "absent"), ModelDir: writeFakeModel(t)})
	if missing.Available() {
		t.Fatal("missing engine must not be available")
	}
	if _, _, err := missing.Rank(context.Background(), decide.HeadCodeRank, "t", []string{"a"}); !errors.Is(err, decide.ErrUnavailable) {
		t.Fatalf("missing error = %v", err)
	}
	noModel := bialy.New(bialy.Config{Binary: writeFakeEngine(t), ModelDir: t.TempDir()})
	if noModel.Available() {
		t.Fatal("an engine without its checkpoint must not be available")
	}
	if _, _, err := noModel.Rank(context.Background(), decide.HeadCodeRank, "t", []string{"a"}); !errors.Is(err, decide.ErrUnavailable) {
		t.Fatalf("no-model error = %v", err)
	}
}

func TestConfigArgsNameTheCheckpointAndHeads(t *testing.T) {
	t.Parallel()
	cfg := bialy.Config{ModelDir: "/m", ModelID: "org/model", Device: "mlx", HeadMaxLen: 512, Metallib: "/r/decide/mlx.metallib", Heads: map[decide.Head]string{
		decide.HeadCodeRank: "/h/code-rank.safetensors",
		decide.HeadTurnLoad: "/h/turn-load.safetensors",
	}}
	got := strings.Join(cfg.Args(), " ")
	want := "serve --model /m --model-id org/model --device mlx --head-max-len 512 --metallib /r/decide/mlx.metallib --head turn-load=/h/turn-load.safetensors --head code-rank=/h/code-rank.safetensors"
	if got != want {
		t.Fatalf("args = %q\nwant %q", got, want)
	}
	if bare := strings.Join(bialy.Config{ModelDir: "/m"}.Args(), " "); bare != "serve --model /m" {
		t.Fatalf("bare args = %q", bare)
	}
}

func TestConfigPrefersTheBundledCheckpoint(t *testing.T) {
	root := t.TempDir()
	t.Setenv(configlayout.EnvEngineRoot, root)
	t.Setenv(bialy.EnvModelDir, "")
	bundled := filepath.Join(root, filepath.FromSlash(bialy.ShippedModel.BundledRel()))

	testutil.FailErr(t, "mkdir partial checkpoint", os.MkdirAll(bundled, 0o755))
	if got := bialy.ConfigFromEnvironment().ModelDir; got == bundled {
		t.Fatalf("an incomplete bundled checkpoint resolved: %s", got)
	}

	layOutModel(t, bundled)
	if got := bialy.ConfigFromEnvironment().ModelDir; got != bundled {
		t.Fatalf("model dir = %q, want the bundled checkpoint %q", got, bundled)
	}

	t.Setenv(bialy.EnvModelDir, "/override")
	if got := bialy.ConfigFromEnvironment().ModelDir; got != "/override" {
		t.Fatalf("model dir = %q, want the override", got)
	}
}

func TestClientRejectsAnExpiredCallerDeadline(t *testing.T) {
	t.Parallel()
	client := bialy.New(fakeConfig(t))
	t.Cleanup(func() { _ = client.Close() })
	testutil.FailErr(t, "Warm", client.Warm(context.Background()))
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if _, _, err := client.Rank(ctx, decide.HeadCodeRank, "t", []string{"a", "b"}); !errors.Is(err, decide.ErrDeadline) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v want deadline", err)
	}
}

func TestCanceledRequestDoesNotStartEngine(t *testing.T) {
	t.Parallel()
	cfg := fakeConfig(t)
	marker := filepath.Join(filepath.Dir(cfg.Binary), "started")
	script := "#!/bin/sh\ntouch \"$(dirname \"$0\")/started\"\n" + fakeEngine
	testutil.FailErr(t, "instrument engine startup", os.WriteFile(cfg.Binary, []byte(script), 0o755))
	client := bialy.New(cfg)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := client.Rank(ctx, decide.HeadCodeRank, "task", []string{"a", "b"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled rank error = %v", err)
	}
	testutil.FailErr(t, "close engine", client.Close())
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("canceled request started engine: %v", err)
	}
}

func TestClientAdmissionRetainsTimedOutNativeWork(t *testing.T) {
	t.Run("encoding failure", func(t *testing.T) {
		client := bialy.New(fakeConfig(t))
		t.Cleanup(func() { _ = client.Close() })
		_, err := client.Decide(t.Context(), decide.HeadTurnLoad, func() {}, map[string]decide.Question{"edit": decide.Noul("edit?")})
		if err == nil {
			t.Fatal("unsupported state encoded successfully")
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		_, _, err = client.Rank(ctx, decide.HeadCodeRank, "after encoding error", []string{"a", "b"})
		testutil.FailErr(t, "admit request after encoding failure", err)
	})
	for _, terminal := range []string{"reply", "exit", "close"} {
		t.Run(terminal, func(t *testing.T) {
			cfg := fakeConfig(t)
			dir := filepath.Dir(cfg.Binary)
			script := strings.Replace(fakeEngine, "while IFS=", `dir=$(dirname "$0")
while IFS=`, 1)
			script = strings.Replace(script, `  case "$line" in`, `  printf '%s\n' "$line" >> "$dir/received"
  case "$line" in
    *'"task":"blocked"'*)
      touch "$dir/blocked"
      printf '{"id":999999,"scores":[8,8]}\n'
      printf '{"id":1,"scores":[9,9]}\n'
      while [ ! -f "$dir/release" ]; do sleep 0.01; done
      if [ -f "$dir/exit" ]; then exit 0; fi
      printf '{"id":%s,"scores":[1.0,2.0]}\n' "$id"
      continue ;;
  esac
  case "$line" in`, 1)
			testutil.FailErr(t, "install gated native engine", os.WriteFile(cfg.Binary, []byte(script), 0o755))
			if terminal == "exit" {
				testutil.FailErr(t, "select native exit", os.WriteFile(filepath.Join(dir, "exit"), nil, 0o600))
			}
			client := bialy.New(cfg)
			t.Cleanup(func() { _ = client.Close() })
			testutil.FailErr(t, "warm native engine", client.Warm(t.Context()))
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			first := make(chan error, 1)
			go func() {
				_, _, err := client.Rank(ctx, decide.HeadCodeRank, "blocked", []string{"a", "b"})
				first <- err
			}()
			waitForEngineMarker(t, filepath.Join(dir, "blocked"))
			if terminal != "exit" {
				cancel()
			}
			err := <-first
			if terminal != "exit" && !errors.Is(err, context.Canceled) {
				t.Fatalf("in-flight cancellation = %v", err)
			}
			if terminal == "exit" && !errors.Is(err, decide.ErrDeadline) {
				t.Fatalf("in-flight deadline = %v", err)
			}
			queued, stop := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer stop()
			_, _, err = client.Rank(queued, decide.HeadCodeRank, "must-not-send", []string{"a", "b"})
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, decide.ErrDeadline) {
				t.Fatalf("queued deadline = %v", err)
			}
			live := make(chan error, 2)
			for range 2 {
				waiting := &admissionContext{Context: t.Context(), observed: make(chan struct{})}
				go func() {
					scores, _, err := client.Rank(waiting, decide.HeadCodeRank, "live", []string{"a", "b"})
					if err == nil && (len(scores) != 2 || scores[1] != 3.7) {
						err = errors.New("live caller received an abandoned request's scores")
					}
					live <- err
				}()
				select {
				case <-waiting.observed:
				case <-time.After(5 * time.Second):
					t.Fatal("caller did not enter process admission")
				}
			}
			// The native process must remain blocked until explicitly released.
			select {
			case err := <-live:
				t.Fatalf("live caller completed before native release: %v", err)
			default:
			}
			if terminal == "close" {
				testutil.FailErr(t, "close occupied native process", client.Close())
			}
			testutil.FailErr(t, "release native request", os.WriteFile(filepath.Join(dir, "release"), nil, 0o600))
			for range 2 {
				select {
				case err := <-live:
					if terminal == "reply" {
						testutil.FailErr(t, "live request after late response", err)
					} else if err == nil {
						t.Fatal("native exit did not fail waiting request")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("native terminal event did not release waiting caller")
				}
			}
			// A dead process is replaceable; a healthy process remains reusable.
			_, _, err = client.Rank(t.Context(), decide.HeadCodeRank, "after", []string{"a", "b"})
			testutil.FailErr(t, "rank after terminal event", err)
			data, err := os.ReadFile(filepath.Join(dir, "received"))
			testutil.FailErr(t, "read actual native requests", err)
			if strings.Contains(string(data), "must-not-send") {
				t.Fatalf("expired queued request reached native engine: %s", data)
			}
		})
	}
}

func waitForEngineMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("native engine did not create %s", path)
		case <-ticker.C:
		}
	}
}

// Done is first read by process admission after Client resolves its process.
type admissionContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *admissionContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}
