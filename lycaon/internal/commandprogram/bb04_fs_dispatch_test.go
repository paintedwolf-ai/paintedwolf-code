package commandprogram

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

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
