package docext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/promptattach/docformat"
	"github.com/tsawler/tabula"
)

const (
	workerArg         = "__pw_document_extract_worker__"
	workerRequestName = "request.json"
)

type workerRequest struct {
	Format docformat.Format
	Input  boundedRequest
}

type workerResponse struct {
	Result       Result
	ErrorCode    string
	ErrorMessage string
}

func init() {
	if len(os.Args) == 2 && os.Args[1] == workerArg {
		os.Exit(runExtractionWorker(os.Stdin))
	}
}

func extractInWorker(ctx context.Context, format docformat.Format, req boundedRequest) (Result, error) {
	executable, err := os.Executable()
	if err != nil {
		return Result{}, attacherr.Unsupported("document worker is unavailable")
	}
	workerDir, err := os.MkdirTemp("", "pw-document-worker-*")
	if err != nil {
		return Result{}, attacherr.Unsupported("document worker staging is unavailable")
	}
	defer func() { _ = os.RemoveAll(workerDir) }()
	requestPath := filepath.Join(workerDir, workerRequestName)
	if err := writeWorkerRequest(requestPath, workerRequest{Format: format, Input: req}); err != nil {
		return Result{}, attacherr.Unsupported("document worker request could not be encoded")
	}
	requestLocation := fseffect.Location{Root: workerDir, Rel: workerRequestName}
	output, exitCode, runErr := execpkg.Run(ctx, executable, []string{workerArg}, execpkg.ExecOpts{
		Launch:         execpkg.HostLaunch("document_extract_worker"),
		NoTimeout:      true,
		MaxOutputBytes: req.Bounds.MaxExtractedBytes + 64<<10,
		Stdin:          &execpkg.StdinSpec{From: &requestLocation},
		AppendEnv: []string{
			"GOMEMLIMIT=" + strconv.FormatInt(req.Bounds.MaxWorkerMemoryBytes, 10) + "B",
			"GOMAXPROCS=2",
		},
	})
	if errors.Is(runErr, execpkg.ErrOutputTruncated) {
		return Result{}, attacherr.TooLarge("document worker output exceeded its boundary")
	}
	if runErr != nil || exitCode != 0 {
		if ctx.Err() != nil {
			return Result{}, attacherr.TooLarge("document parse exceeded time bound")
		}
		return Result{}, attacherr.TooLarge("document parser exceeded its resource boundary")
	}
	var response workerResponse
	if err := json.NewDecoder(bytes.NewReader(output)).Decode(&response); err != nil {
		return Result{}, attacherr.Unsupported("document worker returned an invalid response")
	}
	if response.ErrorCode != "" {
		switch response.ErrorCode {
		case attacherr.CodeTooLarge:
			return Result{}, attacherr.TooLarge(response.ErrorMessage)
		default:
			return Result{}, attacherr.Unsupported(response.ErrorMessage)
		}
	}
	return response.Result, nil
}

func writeWorkerRequest(path string, request workerRequest) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(f).Encode(request)
	closeErr := f.Close()
	return errors.Join(encodeErr, closeErr)
}

func runExtractionWorker(input io.Reader) int {
	var request workerRequest
	if err := json.NewDecoder(input).Decode(&request); err != nil {
		return 2
	}
	workerDir, err := os.MkdirTemp("", "pw-document-worker-output-*")
	if err != nil {
		return 2
	}
	defer func() { _ = os.RemoveAll(workerDir) }()
	applyWorkerMemoryLimit(request.Input.Bounds.MaxWorkerMemoryBytes)
	result, err := extractBuiltInDirect(request.Format, request.Input, workerDir)
	response := workerResponse{Result: result}
	if err != nil {
		response.ErrorCode = attacherr.CodeOf(err)
		if response.ErrorCode == "" {
			response.ErrorCode = attacherr.CodeUnsupported
		}
		var typed *attacherr.Error
		if errors.As(err, &typed) {
			response.ErrorMessage = typed.Message
		} else {
			response.ErrorMessage = err.Error()
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		return 3
	}
	return 0
}

func extractBuiltInDirect(format docformat.Format, req boundedRequest, workerDir string) (Result, error) {
	switch format {
	case docformat.PDF, docformat.DOCX, docformat.XLSX, docformat.PPTX, docformat.ODT:
		return extractTabulaDirect(format, req, workerDir)
	case docformat.ODS, docformat.ODP:
		return extractODFDirect(format, req)
	case docformat.RTF:
		return extractRTFDirect(req)
	default:
		return Result{}, attacherr.Unsupported("document worker does not support this format")
	}
}

func extractTabulaDirect(format docformat.Format, req boundedRequest, workerDir string) (Result, error) {
	path, cleanup, err := writeTemp(workerDir, format, req.Bytes)
	if err != nil {
		return Result{}, attacherr.Unsupported("could not stage document bytes")
	}
	defer cleanup()
	ex := tabula.Open(path)
	count, err := ex.PageCount()
	if err != nil {
		return Result{}, attacherr.Unsupported("document extract failed")
	}
	maxUnits := req.Bounds.MaxPages
	if format == docformat.PPTX {
		maxUnits = req.Bounds.MaxSlides
	}
	if count > maxUnits {
		return Result{}, attacherr.TooLarge("document exceeds page/slide cap")
	}
	text, _, err := ex.Text()
	if err != nil {
		return Result{}, attacherr.Unsupported("document extract failed")
	}
	text = strings.TrimSpace(text)
	text = truncateExtractedText(text, req.Bounds.MaxExtractedBytes)
	out := Result{Text: text, UnitCount: count}
	if text == "" && (format == docformat.PDF || format == docformat.PPTX) {
		out.ScannedNoText = true
	}
	return out, nil
}

func truncateExtractedText(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	kept := maxBytes
	for kept > 0 && kept < len(text) && !utf8.RuneStart(text[kept]) {
		kept--
	}
	return text[:kept]
}
