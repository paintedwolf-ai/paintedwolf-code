package commandprogram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

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
