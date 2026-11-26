package commandprogram

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

const (
	bb04SeedCount         = 256
	bb04FamilyAGenVersion = "bb04-family-a-shared-shell-semantics-v1"
	bb04FamilyBGenVersion = "bb04-family-b-effect-sequences-v1"
)

func bb04GeneratorDigest(version string) string {
	sum := sha256.Sum256([]byte(version))
	return hex.EncodeToString(sum[:8])
}

// ---------------------------------------------------------------------------
// Family A: Generated shared-shell-semantics differential vs system Bash
// ---------------------------------------------------------------------------

type bb04FSDispatch struct {
	root string
	cwd  string
}

func (d *bb04FSDispatch) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(d.cwd, path)
}

func (d *bb04FSDispatch) Fork(context.Context) (Dispatch, func(), error) {
	return &bb04FSDispatch{root: d.root, cwd: d.cwd}, func() {}, nil
}

func (d *bb04FSDispatch) PredicateFiles() PredicateFiles {
	return bb04FSPredicates{root: d.cwd}
}

func (d *bb04FSDispatch) Redirections() (InputRedirection, OutputPublication) {
	return bb04FSInput{cwd: d.cwd}, bb04FSPublication{cwd: d.cwd}
}

func (d *bb04FSDispatch) InvokeEvaluator(ctx context.Context, request OperationRequest, streams Streams, body func(context.Context, Streams) (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error) {
	if request.StreamPreparation != nil {
		var err error
		streams, err = request.StreamPreparation.PrepareStreams(ctx)
		if err != nil {
			var failure toolcontract.ProducerFailure
			if errors.As(err, &failure) {
				return failure.ProducerResult(), err
			}
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
		}
	}
	return body(ctx, streams)
}

func (d *bb04FSDispatch) Launch(ctx context.Context, request OperationRequest, streams Streams) (toolcontract.ProducerResult, error) {
	if request.StreamPreparation != nil {
		var err error
		streams, err = request.StreamPreparation.PrepareStreams(ctx)
		if err != nil {
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
		}
	}
	argv := request.Argv.Values()
	program := argv
	if len(program) >= 2 && program[0] == "external" && program[1] == "--" {
		program = program[2:]
	}
	if len(program) >= 2 && program[0] == "fail" {
		code, _ := strconv.Atoi(program[1])
		return toolcontract.ProducerResult{Status: code, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: code == 0, EffectCertainty: toolcontract.EffectCompleted}, nil
	}
	return toolcontract.ProducerResult{Status: 127, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, fmt.Errorf("unsupported external %v", program)
}

func (d *bb04FSDispatch) Invoke(ctx context.Context, request OperationRequest, streams Streams) (toolcontract.ProducerResult, error) {
	if request.StreamPreparation != nil {
		var err error
		streams, err = request.StreamPreparation.PrepareStreams(ctx)
		if err != nil {
			var failure toolcontract.ProducerFailure
			if errors.As(err, &failure) {
				return failure.ProducerResult(), err
			}
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
		}
	}
	if err := request.Validate(); err != nil {
		return toolcontract.ProducerResult{}, err
	}
	argv := request.Argv.Values()
	writeOut := func(s string) (toolcontract.ProducerResult, error) {
		if _, err := io.WriteString(streams.Output, s); err != nil {
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
		}
		return completed(0), nil
	}
	switch argv[0] {
	case "echo":
		return writeOut(strings.Join(argv[1:], " ") + "\n")
	case "printf":
		if len(argv) < 2 {
			return completed(0), nil
		}
		out := bb04FormatPrintf(argv[1], argv[2:])
		return writeOut(out)
	case "cat":
		var files []string
		for _, a := range argv[1:] {
			if a == "--" {
				continue
			}
			files = append(files, a)
		}
		if len(files) == 0 {
			data, err := io.ReadAll(streams.Input)
			if err != nil {
				return ownerResult(err), err
			}
			return writeOut(string(data))
		}
		var buf bytes.Buffer
		for _, f := range files {
			raw, err := os.ReadFile(d.resolve(f))
			if err != nil {
				_, _ = fmt.Fprintf(streams.Error, "cat: %s: No such file or directory\n", f)
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: false, EffectCertainty: toolcontract.NoEffect}, nil
			}
			buf.Write(raw)
		}
		return writeOut(buf.String())
	case "grep":
		if len(argv) < 2 {
			return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerCompleted, Complete: false, EffectCertainty: toolcontract.NoEffect}, nil
		}
		pattern := argv[1]
		var data []byte
		var err error
		if len(argv) >= 3 {
			data, err = os.ReadFile(d.resolve(argv[2]))
			if err != nil {
				return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerCompleted, Complete: false, EffectCertainty: toolcontract.NoEffect}, nil
			}
		} else {
			data, err = io.ReadAll(streams.Input)
			if err != nil {
				return ownerResult(err), err
			}
		}
		var matched strings.Builder
		for _, line := range strings.SplitAfter(string(data), "\n") {
			if line != "" && strings.Contains(line, pattern) {
				matched.WriteString(line)
			}
		}
		res := toolcontract.ProducerResult{
			Status:              1,
			Terminal:            toolcontract.ProducerCompleted,
			Complete:            true,
			PublicationEligible: true,
			PublicationBasis:    toolcontract.NativeComparisonPublication,
			EffectCertainty:     toolcontract.NoEffect,
		}
		if matched.Len() > 0 {
			res.Status = 0
			if _, err := io.WriteString(streams.Output, matched.String()); err != nil {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
			}
		}
		return res, nil
	case "head":
		n := 10
		args := argv[1:]
		if len(args) >= 2 && args[0] == "-n" {
			if parsed, err := strconv.Atoi(args[1]); err == nil && parsed >= 0 {
				n = parsed
			}
			args = args[2:]
		}
		var r io.ReadCloser = streams.Input
		closeAfter := false
		if len(args) >= 1 {
			f, err := os.Open(d.resolve(args[0]))
			if err != nil {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
			}
			r = f
			closeAfter = true
		}
		br := bufio.NewReader(r)
		var out strings.Builder
		for i := 0; i < n; i++ {
			line, err := br.ReadString('\n')
			if len(line) > 0 {
				out.WriteString(line)
			}
			if err != nil {
				break
			}
		}
		if closeAfter {
			_ = r.Close()
		} else {
			_ = streams.Input.Close()
		}
		return writeOut(out.String())
	case "tr":
		data, err := io.ReadAll(streams.Input)
		if err != nil {
			return ownerResult(err), err
		}
		text := string(data)
		if len(argv) == 3 && argv[1] == "a-z" && argv[2] == "A-Z" {
			text = strings.ToUpper(text)
		} else if len(argv) == 3 && argv[1] == "A-Z" && argv[2] == "a-z" {
			text = strings.ToLower(text)
		}
		return writeOut(text)
	default:
		return toolcontract.ProducerResult{}, fmt.Errorf("bb04FSDispatch: unsupported command %q", argv[0])
	}
}

// bb04FormatPrintf implements the exact POSIX printf subset emitted by our
// differential generator (%s, %d, \n, \t, \\, %% with format reuse across args).
func bb04FormatPrintf(format string, args []string) string {
	var out strings.Builder
	argIdx := 0
	for {
		consumed := false
		for i := 0; i < len(format); i++ {
			if format[i] == '\\' && i+1 < len(format) {
				i++
				switch format[i] {
				case 'n':
					out.WriteByte('\n')
				case 't':
					out.WriteByte('\t')
				case '\\':
					out.WriteByte('\\')
				default:
					out.WriteByte('\\')
					out.WriteByte(format[i])
				}
				continue
			}
			if format[i] == '%' && i+1 < len(format) {
				i++
				switch format[i] {
				case '%':
					out.WriteByte('%')
				case 's':
					consumed = true
					if argIdx < len(args) {
						out.WriteString(args[argIdx])
						argIdx++
					}
				case 'd':
					consumed = true
					v := 0
					if argIdx < len(args) {
						v, _ = strconv.Atoi(args[argIdx])
						argIdx++
					}
					out.WriteString(strconv.Itoa(v))
				default:
					out.WriteByte('%')
					out.WriteByte(format[i])
				}
				continue
			}
			out.WriteByte(format[i])
		}
		if !consumed || argIdx >= len(args) {
			break
		}
	}
	return out.String()
}

type bb04FSPredicates struct{ root string }

func (p bb04FSPredicates) TestPath(_ context.Context, _ toolcontract.OperationInvocation, op, operand string) (bool, error) {
	target := operand
	if !filepath.IsAbs(target) {
		target = filepath.Join(p.root, operand)
	}
	info, err := os.Stat(target)
	if err != nil {
		return false, nil
	}
	switch op {
	case "-e":
		return true, nil
	case "-f":
		return info.Mode().IsRegular(), nil
	case "-d":
		return info.IsDir(), nil
	default:
		return false, nil
	}
}

type bb04FSInput struct{ cwd string }

func (i bb04FSInput) OpenInput(_ context.Context, dest InputDestination) (io.ReadCloser, error) {
	path := dest.Target.Value
	if !filepath.IsAbs(path) {
		path = filepath.Join(i.cwd, path)
	}
	return os.Open(path)
}

type bb04FSPublication struct{ cwd string }

type bb04FSStaged struct {
	path   string
	append bool
	buf    bytes.Buffer
}

func (p bb04FSPublication) Open(_ context.Context, dest OutputDestination) (StagedOutput, error) {
	path := dest.Target.Value
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.cwd, path)
	}
	return &bb04FSStaged{
		path:   path,
		append: strings.HasSuffix(dest.Redirect.Operator, ">>"),
	}, nil
}

func (s *bb04FSStaged) Write(b []byte) (int, error) { return s.buf.Write(b) }

func (s *bb04FSStaged) Publish(context.Context) (OutputPublished, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if s.append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(s.path, flags, 0o644)
	if err != nil {
		return OutputPublished{}, err
	}
	_, werr := f.Write(s.buf.Bytes())
	cerr := f.Close()
	if werr != nil {
		return OutputPublished{}, werr
	}
	if cerr != nil {
		return OutputPublished{}, cerr
	}
	return OutputPublished{PublishedRevision: "fs"}, nil
}

func (s *bb04FSStaged) Abandon(context.Context) error { return nil }

func bb04SeedTree(t testing.TB, dir string) {
	t.Helper()
	files := map[string]string{
		"seed.txt":  "alpha one\nbeta two\ngamma three\n",
		"quote.txt": "don't split spaces\nsecond line\n",
		"empty.txt": "",
	}
	for name, body := range files {
		testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
}

func bb04SnapshotTree(t testing.TB, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	testutil.FailErr(t, "walk "+dir, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(raw)
		return nil
	}))
	return out
}

type bb04RunResult struct {
	status int
	stdout string
	stderr string
	tree   map[string]string
	err    error
}

func bb04RunCommandProgram(t testing.TB, dir, program string) bb04RunResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := fixtureInvocation()
	root.SourceUnitID = "program"
	dispatch := &bb04FSDispatch{root: dir, cwd: dir}
	mem := fixtureScratch(t)
	ports := EvaluationPorts{
		Invocation:  root,
		Dispatch:    dispatch,
		Binder:      CatalogBinder{},
		Files:       memoryFiles{},
		Publication: bb04FSPublication{cwd: dir},
		Input:       bb04FSInput{cwd: dir},
		Predicates:  PredicateRuntime{Files: bb04FSPredicates{root: dir}},
		Supervision: &fakeJobs{},
		Memory:      mem,
		Streams:     Streams{Input: NullInput(), Output: &stdout, Error: &stderr},
	}
	ctx := t.Context()
	parsed, err := commandsurface.ParseAdmitted(ctx, mem, []byte(program), commandsurface.ParseOptions{SourceName: "program"})
	if err != nil {
		return bb04RunResult{err: err}
	}
	defer parsed.Close()
	lease, err := parsed.SnapshotWithMemory(ctx, mem)
	if err != nil {
		return bb04RunResult{err: err}
	}
	defer lease.Close()
	out, evalErr := Evaluate(ctx, lease.Plan, ports)
	return bb04RunResult{
		status: out.Status,
		stdout: stdout.String(),
		stderr: stderr.String(),
		tree:   bb04SnapshotTree(t, dir),
		err:    evalErr,
	}
}

func bb04RunBash(t testing.TB, bashPath, dir, program string, pipefail bool) bb04RunResult {
	t.Helper()
	script := program
	if pipefail {
		script = "set -o pipefail\n" + program
	}
	cmd := exec.CommandContext(t.Context(), bashPath, "--noprofile", "--norc", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{"LC_ALL=C", "LANG=C", "PATH=" + os.Getenv("PATH")}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	status := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			status = exitErr.ExitCode()
		} else {
			return bb04RunResult{err: err}
		}
	}
	return bb04RunResult{
		status: status,
		stdout: stdout.String(),
		stderr: stderr.String(),
		tree:   bb04SnapshotTree(t, dir),
	}
}

func bb04GenerateFamilyA(seed uint64) []string {
	rng := rand.New(rand.NewPCG(seed, 0xbb04a11ce))
	count := 1 + rng.IntN(8)
	literals := []string{
		`'plain'`,
		`'two words'`,
		`'don'\''t stop'`,
		`'a|b;c&d'`,
		`''`,
		`"quoted $v"`,
	}
	vars := []string{"v", "k", "item"}
	targets := []string{"out_a.txt", "out_b.txt"}
	stmts := make([]string, 0, count)
	stmts = append(stmts, fmt.Sprintf("v=%s", literals[rng.IntN(4)]))
	for len(stmts) < count {
		switch rng.IntN(9) {
		case 0: // quoted literal + apostrophe + spaces
			lit := literals[rng.IntN(len(literals))]
			stmts = append(stmts, fmt.Sprintf("printf '[%%s]\\n' %s", lit))
		case 1: // variable assignment + parameter expansion
			name := vars[rng.IntN(len(vars))]
			lit := literals[rng.IntN(5)]
			stmts = append(stmts, fmt.Sprintf("%s=%s; printf '%%s:%%s\\n' \"${%s:-fallback}\" \"${#%s}\"", name, lit, name, name))
		case 2: // if/elif/else branch with file/string predicate
			if rng.IntN(2) == 0 {
				stmts = append(stmts, `if [ -f seed.txt ]; then printf 'has-seed\n'; else printf 'no-seed\n'; fi`)
			} else {
				stmts = append(stmts, `if [ "$v" = 'plain' ]; then printf 'is-plain\n'; else printf 'other\n'; fi`)
			}
		case 3: // case branch
			stmts = append(stmts, `case "$v" in plain) printf 'c:plain\n';; *stop*) printf 'c:apos\n';; *) printf 'c:other\n';; esac`)
		case 4: // finite for loop with quoted items
			stmts = append(stmts, `for x in 'one' 'two three' 'it'\''s'; do printf 'f:%s\n' "$x"; done`)
		case 5: // finite while loop with arithmetic
			limit := 1 + rng.IntN(3)
			stmts = append(stmts, fmt.Sprintf(`i=0; while [ "$i" -lt %d ]; do printf 'w:%%d\n' "$i"; i=$((i + 1)); done`, limit))
		case 6: // command substitution
			if rng.IntN(2) == 0 {
				stmts = append(stmts, `sub=$(printf 'sub %s' "$v"); printf 's:%s\n' "$sub"`)
			} else {
				stmts = append(stmts, `sub=$(cat seed.txt | grep beta); printf 'g:%s\n' "$sub"`)
			}
		case 7: // pipeline (matching or early-close or transform)
			switch rng.IntN(3) {
			case 0:
				stmts = append(stmts, `cat seed.txt | grep alpha | tr a-z A-Z`)
			case 1:
				stmts = append(stmts, `cat seed.txt | head -n 1`)
			default:
				stmts = append(stmts, `printf 'line1\nline2\n' | grep line2 | cat`)
			}
		case 8: // redirects (replace, append, input, stderr)
			target := targets[rng.IntN(len(targets))]
			switch rng.IntN(4) {
			case 0:
				stmts = append(stmts, fmt.Sprintf(`printf 'r:%%s\n' "$v" > %s`, target))
			case 1:
				stmts = append(stmts, fmt.Sprintf(`printf 'a:%%s\n' "$v" >> %s`, target))
			case 2:
				stmts = append(stmts, `cat < quote.txt | head -n 1`)
			default:
				stmts = append(stmts, `printf 'err-note\n' >&2`)
			}
		}
	}
	return stmts
}

func bb04ResultsEqual(a, b bb04RunResult) (bool, string) {
	if a.err != nil || b.err != nil {
		return false, fmt.Sprintf("err eval=%v bash=%v", a.err, b.err)
	}
	if a.status != b.status {
		return false, fmt.Sprintf("status eval=%d bash=%d", a.status, b.status)
	}
	if a.stdout != b.stdout {
		return false, fmt.Sprintf("stdout eval=%q bash=%q", a.stdout, b.stdout)
	}
	if a.stderr != b.stderr {
		return false, fmt.Sprintf("stderr eval=%q bash=%q", a.stderr, b.stderr)
	}
	if len(a.tree) != len(b.tree) {
		return false, fmt.Sprintf("tree keys eval=%v bash=%v", bb04SortedKeys(a.tree), bb04SortedKeys(b.tree))
	}
	for k, va := range a.tree {
		if vb, ok := b.tree[k]; !ok || va != vb {
			return false, fmt.Sprintf("file %q eval=%q bash=%q (ok=%v)", k, va, vb, ok)
		}
	}
	return true, ""
}

func bb04SortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// bb04MinimizeStatements shrinks a failing statement list to a 1-minimal
// prefix/subset that still reproduces a differential mismatch.
func bb04MinimizeStatements(stmts []string, diverges func(string) bool) string {
	cur := append([]string(nil), stmts...)
	for changed := true; changed && len(cur) > 1; {
		changed = false
		for i := 0; i < len(cur); i++ {
			candidate := make([]string, 0, len(cur)-1)
			candidate = append(candidate, cur[:i]...)
			candidate = append(candidate, cur[i+1:]...)
			if diverges(strings.Join(candidate, "\n")) {
				cur = candidate
				changed = true
				break
			}
		}
	}
	return strings.Join(cur, "\n")
}

func TestBB04GeneratedSharedShellSemanticsDifferential(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available on PATH")
	}
	verOut, err := exec.CommandContext(t.Context(), bashPath, "--version").Output()
	testutil.FailErr(t, "query bash --version", err)
	bashVersion := strings.TrimSpace(strings.Split(string(verOut), "\n")[0])
	digest := bb04GeneratorDigest(bb04FamilyAGenVersion)
	t.Logf("Family A differential: seeds=0..%d generator_digest=%s bash=%q", bb04SeedCount-1, digest, bashVersion)

	type failure struct {
		seed      uint64
		diff      string
		program   string
		minimized string
	}

	workers := min(runtime.GOMAXPROCS(0), 8)
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan uint64, bb04SeedCount)
	for seed := uint64(0); seed < bb04SeedCount; seed++ {
		jobs <- seed
	}
	close(jobs)

	var (
		mu       sync.Mutex
		failures []failure
		wg       sync.WaitGroup
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for seed := range jobs {
				stmts := bb04GenerateFamilyA(seed)
				program := strings.Join(stmts, "\n")
				runPair := func(src string) (bool, string) {
					evalDir := t.TempDir()
					bashDir := t.TempDir()
					bb04SeedTree(t, evalDir)
					bb04SeedTree(t, bashDir)
					ev := bb04RunCommandProgram(t, evalDir, src)
					ba := bb04RunBash(t, bashPath, bashDir, src, true)
					return bb04ResultsEqual(ev, ba)
				}
				if ok, diff := runPair(program); !ok {
					minimized := bb04MinimizeStatements(stmts, func(src string) bool {
						ok, _ := runPair(src)
						return !ok
					})
					mu.Lock()
					failures = append(failures, failure{
						seed:      seed,
						diff:      diff,
						program:   program,
						minimized: minimized,
					})
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if len(failures) > 0 {
		sort.Slice(failures, func(i, j int) bool { return failures[i].seed < failures[j].seed })
		first := failures[0]
		t.Fatalf("Family A differential found %d/%d divergences (generator_digest=%s bash=%q); first seed=%d diff=%s\n--- program ---\n%s\n--- minimized ---\n%s",
			len(failures), bb04SeedCount, digest, bashVersion, first.seed, first.diff, first.program, first.minimized)
	}
}

// TestBB04PipefailContractDifferential explicitly pins the documented pipefail
// contract: commandprogram always reports the rightmost nonzero pipeline stage
// (matching bash with `set -o pipefail`), admits `set -o pipefail`, and refuses
// `set +o pipefail` at admission.
func TestBB04PipefailContractDifferential(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available on PATH")
	}
	program := "grep nonexistent seed.txt | cat"
	evalDir, bashOnDir, bashOffDir := t.TempDir(), t.TempDir(), t.TempDir()
	bb04SeedTree(t, evalDir)
	bb04SeedTree(t, bashOnDir)
	bb04SeedTree(t, bashOffDir)

	ev := bb04RunCommandProgram(t, evalDir, program)
	baOn := bb04RunBash(t, bashPath, bashOnDir, program, true)
	baOff := bb04RunBash(t, bashPath, bashOffDir, program, false)

	if ok, diff := bb04ResultsEqual(ev, baOn); !ok {
		t.Fatalf("pipefail-on differential mismatch: %s", diff)
	}
	if ev.status != 1 || baOff.status != 0 {
		t.Fatalf("expected product-defined pipefail contrast: eval=%d bash(pipefail-on)=%d bash(pipefail-off)=%d",
			ev.status, baOn.status, baOff.status)
	}

	// `set -o pipefail` is admitted; `set +o pipefail` is refused.
	if res := bb04RunCommandProgram(t, evalDir, "set -o pipefail; grep nonexistent seed.txt | cat"); res.err != nil || res.status != 1 {
		t.Fatalf("set -o pipefail failed: %+v", res)
	}
	mem := fixtureScratch(t)
	if _, err := commandsurface.ParseAdmitted(t.Context(), mem, []byte("set +o pipefail; echo ok"), commandsurface.ParseOptions{SourceName: "pipefail.pw"}); err == nil {
		t.Fatal("set +o pipefail must be refused by version 1 syntax admission")
	}
}

// ---------------------------------------------------------------------------
// Family B: Generated effect sequences vs independent file-state oracle
// ---------------------------------------------------------------------------

type bb04OracleFileState struct {
	bytes map[string][]byte
	rev   map[string]int
}

func newBB04OracleFileState() *bb04OracleFileState {
	return &bb04OracleFileState{
		bytes: map[string][]byte{},
		rev:   map[string]int{},
	}
}

type bb04OraclePublication struct {
	mu        sync.Mutex
	files     map[string][]byte
	revs      map[string]int
	committed int
	withheld  int
	refused   int
}

func newBB04OraclePublication() *bb04OraclePublication {
	return &bb04OraclePublication{
		files: map[string][]byte{},
		revs:  map[string]int{},
	}
}

type bb04OracleStaged struct {
	pub    *bb04OraclePublication
	target string
	append bool
	buf    bytes.Buffer
}

func (p *bb04OraclePublication) Open(_ context.Context, dest OutputDestination) (StagedOutput, error) {
	if dest.Target.Value == "refused" {
		p.mu.Lock()
		p.refused++
		p.mu.Unlock()
		return nil, &codedError{code: "COMMAND_PATH_REFUSED"}
	}
	return &bb04OracleStaged{
		pub:    p,
		target: dest.Target.Value,
		append: strings.HasSuffix(dest.Redirect.Operator, ">>"),
	}, nil
}

func (s *bb04OracleStaged) Write(b []byte) (int, error) { return s.buf.Write(b) }

func (s *bb04OracleStaged) Publish(context.Context) (OutputPublished, error) {
	s.pub.mu.Lock()
	defer s.pub.mu.Unlock()
	if s.append {
		s.pub.files[s.target] = append(append([]byte(nil), s.pub.files[s.target]...), s.buf.Bytes()...)
	} else {
		s.pub.files[s.target] = append([]byte(nil), s.buf.Bytes()...)
	}
	s.pub.revs[s.target]++
	s.pub.committed++
	return OutputPublished{PublishedRevision: fmt.Sprintf("rev-%d", s.pub.revs[s.target])}, nil
}

func (s *bb04OracleStaged) Abandon(context.Context) error {
	s.pub.mu.Lock()
	defer s.pub.mu.Unlock()
	s.pub.withheld++
	return nil
}

type bb04EffectStepKind int

const (
	bb04StepCreate bb04EffectStepKind = iota
	bb04StepAppend
	bb04StepReplace
	bb04StepProducerFail
	bb04StepDeny
	bb04StepRetryFromKnown
)

type bb04EffectStep struct {
	kind    bb04EffectStepKind
	target  string
	payload string
}

func bb04GenerateFamilyB(seed uint64) []bb04EffectStep {
	rng := rand.New(rand.NewPCG(seed, 0xbb04b00b))
	count := 1 + rng.IntN(8)
	targets := []string{"alpha.txt", "beta.txt", "gamma.txt"}
	payloads := []string{"v1\n", "line two\n", "it's ok\n", "payload-4\n", "final\n"}
	steps := make([]bb04EffectStep, 0, count)
	for i := 0; i < count; i++ {
		kind := bb04EffectStepKind(rng.IntN(6))
		target := targets[rng.IntN(len(targets))]
		payload := payloads[rng.IntN(len(payloads))]
		if kind == bb04StepDeny {
			target = "refused"
		}
		steps = append(steps, bb04EffectStep{kind: kind, target: target, payload: payload})
	}
	return steps
}

func bb04ExecuteEffectSequence(t testing.TB, steps []bb04EffectStep) error {
	t.Helper()
	pub := newBB04OraclePublication()
	oracle := newBB04OracleFileState()
	wantCommitted, wantWithheld, wantRefused := 0, 0, 0

	runProgram := func(src string) (ProgramOutcome, error) {
		var stdout, stderr bytes.Buffer
		root := fixtureInvocation()
		root.SourceUnitID = "program"
		mem := fixtureScratch(t)
		log := &dispatchLog{files: map[string]string{}}
		ports := EvaluationPorts{
			Invocation:  root,
			Dispatch:    &fakeDispatch{log: log, cwd: "/project"},
			Binder:      CatalogBinder{},
			Files:       memoryFiles{},
			Publication: pub,
			Input:       fakeInput{},
			Supervision: &fakeJobs{},
			Memory:      mem,
			Streams:     Streams{Input: NullInput(), Output: &stdout, Error: &stderr},
		}
		parsed, err := commandsurface.ParseAdmitted(t.Context(), mem, []byte(src), commandsurface.ParseOptions{SourceName: "program"})
		if err != nil {
			return ProgramOutcome{}, err
		}
		defer parsed.Close()
		lease, err := parsed.SnapshotWithMemory(t.Context(), mem)
		if err != nil {
			return ProgramOutcome{}, err
		}
		defer lease.Close()
		return Evaluate(t.Context(), lease.Plan, ports)
	}

	for idx, step := range steps {
		quoted := "'" + strings.ReplaceAll(step.payload, "'", `'\''`) + "'"
		switch step.kind {
		case bb04StepCreate, bb04StepReplace, bb04StepRetryFromKnown:
			src := fmt.Sprintf("echo %s > %s", quoted, step.target)
			out, err := runProgram(src)
			if err != nil || out.Status != 0 || out.Stop != nil {
				return fmt.Errorf("step %d (%s): unexpected err=%v status=%d stop=%+v", idx, src, err, out.Status, out.Stop)
			}
			oracle.bytes[step.target] = []byte(step.payload + "\n")
			oracle.rev[step.target]++
			wantCommitted++

		case bb04StepAppend:
			src := fmt.Sprintf("echo %s >> %s", quoted, step.target)
			out, err := runProgram(src)
			if err != nil || out.Status != 0 || out.Stop != nil {
				return fmt.Errorf("step %d (%s): unexpected err=%v status=%d stop=%+v", idx, src, err, out.Status, out.Stop)
			}
			oracle.bytes[step.target] = append(append([]byte(nil), oracle.bytes[step.target]...), []byte(step.payload+"\n")...)
			oracle.rev[step.target]++
			wantCommitted++

		case bb04StepProducerFail:
			// Producer emits a prefix then fails with status 3 -> publication must be
			// withheld and any prior bytes/revision for step.target must survive intact.
			src := fmt.Sprintf("{ echo %s; external -- fail 3; } > %s", quoted, step.target)
			out, err := runProgram(src)
			if err != nil || out.Status != 3 || out.Stop != nil {
				return fmt.Errorf("step %d (%s): want status 3, got err=%v status=%d stop=%+v", idx, src, err, out.Status, out.Stop)
			}
			wantWithheld++

		case bb04StepDeny:
			// Refused destination stops the program; a trailing write must not execute.
			src := fmt.Sprintf("echo %s > refused; echo never > alpha.txt", quoted)
			out, err := runProgram(src)
			var coded *codedError
			if !errors.As(err, &coded) || coded.code != "COMMAND_PATH_REFUSED" || out.Stop == nil || out.Stop.Kind != StopAuthority {
				return fmt.Errorf("step %d (%s): want COMMAND_PATH_REFUSED authority stop, got err=%v stop=%+v", idx, src, err, out.Stop)
			}
			wantRefused++
		}

		// Verify independent file-state oracle after every single step.
		pub.mu.Lock()
		gotCommitted, gotWithheld, gotRefused := pub.committed, pub.withheld, pub.refused
		if gotCommitted != wantCommitted || gotWithheld != wantWithheld || gotRefused != wantRefused {
			pub.mu.Unlock()
			return fmt.Errorf("step %d counters: got committed=%d withheld=%d refused=%d; want %d/%d/%d",
				idx, gotCommitted, gotWithheld, gotRefused, wantCommitted, wantWithheld, wantRefused)
		}
		if len(pub.files) != len(oracle.bytes) {
			pub.mu.Unlock()
			return fmt.Errorf("step %d file set size=%d want=%d", idx, len(pub.files), len(oracle.bytes))
		}
		for name, wantBytes := range oracle.bytes {
			if !slices.Equal(pub.files[name], wantBytes) {
				gotBytes := pub.files[name]
				pub.mu.Unlock()
				return fmt.Errorf("step %d file %q bytes=%q want=%q", idx, name, gotBytes, wantBytes)
			}
			if pub.revs[name] != oracle.rev[name] {
				gotRev := pub.revs[name]
				pub.mu.Unlock()
				return fmt.Errorf("step %d file %q rev=rev-%d want=rev-%d", idx, name, gotRev, oracle.rev[name])
			}
		}
		pub.mu.Unlock()
	}
	return nil
}

func TestBB04GeneratedEffectSequencesOracle(t *testing.T) {
	digest := bb04GeneratorDigest(bb04FamilyBGenVersion)
	t.Logf("Family B file-state oracle: seeds=0..%d generator_digest=%s", bb04SeedCount-1, digest)
	for seed := uint64(0); seed < bb04SeedCount; seed++ {
		steps := bb04GenerateFamilyB(seed)
		if err := bb04ExecuteEffectSequence(t, steps); err != nil {
			t.Fatalf("Family B oracle divergence (seed=%d generator_digest=%s steps=%+v): %v", seed, digest, steps, err)
		}
	}
}

func TestBB04EffectOracleExplicitTransitions(t *testing.T) {
	steps := []bb04EffectStep{
		{kind: bb04StepCreate, target: "alpha.txt", payload: "initial"},
		{kind: bb04StepAppend, target: "alpha.txt", payload: "appended"},
		{kind: bb04StepProducerFail, target: "alpha.txt", payload: "must-not-publish"},
		{kind: bb04StepDeny, target: "refused", payload: "denied"},
		{kind: bb04StepReplace, target: "alpha.txt", payload: "replaced"},
		{kind: bb04StepRetryFromKnown, target: "beta.txt", payload: "retried"},
	}
	if err := bb04ExecuteEffectSequence(t, steps); err != nil {
		t.Fatalf("explicit transition sequence failed: %v", err)
	}
}
