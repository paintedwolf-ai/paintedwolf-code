package commandprogram

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

type closeCheckingInput struct {
	reader *strings.Reader
	closed bool
}

func (c *closeCheckingInput) Read(p []byte) (int, error) {
	if c.closed {
		return 0, os.ErrClosed
	}
	return c.reader.Read(p)
}

func (c *closeCheckingInput) Close() error {
	c.closed = true
	return nil
}

type closeCheckingInputOwner struct {
	inputs map[string]*closeCheckingInput
}

func (o *closeCheckingInputOwner) OpenInput(_ context.Context, destination InputDestination) (io.ReadCloser, error) {
	in, ok := o.inputs[destination.Target.Value]
	if !ok {
		return nil, errors.New("no such input")
	}
	return in, nil
}

type stdinClosingDispatch struct {
	Dispatch
}

func (d stdinClosingDispatch) Invoke(ctx context.Context, request OperationRequest, streams Streams) (toolcontract.ProducerResult, error) {
	res, err := d.Dispatch.Invoke(ctx, request, streams)
	if streams.Input != nil {
		_ = streams.Input.Close()
	}
	return res, err
}

func TestRedirectedLoopInputSurvivesChildClosingStdin(t *testing.T) {
	f := newEvalFixture(t)
	in := &closeCheckingInput{reader: strings.NewReader("one\ntwo\nthree\n")}
	f.ports.Input = &closeCheckingInputOwner{inputs: map[string]*closeCheckingInput{"lines.txt": in}}
	f.ports.Dispatch = stdinClosingDispatch{Dispatch: f.ports.Dispatch}
	out, err := f.run(t, t.Context(), "while read -r line; do echo \"$line\"; done < lines.txt")
	if err != nil || out.Stop != nil || out.Status != 0 {
		t.Fatalf("out=%+v error=%v", out, err)
	}
	if got := f.stdout.String(); got != "one\ntwo\nthree\n" {
		t.Fatalf("stdout=%q, want %q", got, "one\ntwo\nthree\n")
	}
	if !in.closed {
		t.Fatal("descriptor scope did not close redirected input on exit")
	}
}

func TestDeferredDescriptorsExcludeContainingSubstitution(t *testing.T) {
	f := newEvalFixture(t)
	out, err := f.run(t, t.Context(), "echo $(echo value) > out; echo $(fail 1) > absent; echo $?")
	if err != nil || out.Stop != nil || f.stdout.String() != "1\n" {
		t.Fatalf("out=%+v stdout=%q error=%v", out, f.stdout.String(), err)
	}
	if f.publication.published["out"] != "value\n" {
		t.Fatalf("published=%v", f.publication.published)
	}
	if _, exists := f.publication.published["absent"]; exists {
		t.Fatalf("prevented containing command published: %v", f.publication.published)
	}
}

func TestDeferredDescriptorLivesThroughBackgroundChildren(t *testing.T) {
	for _, command := range []string{"echo late", "fail 1"} {
		t.Run(command, func(t *testing.T) {
			f := newEvalFixture(t)
			out, err := f.run(t, t.Context(), "{ "+command+" & } > out; echo foreground")
			if err != nil || out.Stop != nil || f.stdout.String() != "foreground\n" {
				t.Fatalf("out=%+v stdout=%q error=%v", out, f.stdout.String(), err)
			}
			if len(f.publication.published) != 0 {
				t.Fatalf("published before child completed: %v", f.publication.published)
			}
			result, err := f.jobs.runs[0](context.Background(), Streams{Input: NullInput(), Output: io.Discard, Error: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			if command == "echo late" && (result.Status != 0 || f.publication.published["out"] != "late\n") {
				t.Fatalf("result=%+v published=%v", result, f.publication.published)
			}
			if command == "fail 1" && (result.Status != 1 || len(f.publication.published) != 0 || len(f.publication.abandoned) != 1) {
				t.Fatalf("result=%+v published=%v abandoned=%v", result, f.publication.published, f.publication.abandoned)
			}
		})
	}
}
