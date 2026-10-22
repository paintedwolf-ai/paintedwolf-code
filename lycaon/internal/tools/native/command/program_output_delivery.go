package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

const (
	maxRetainedProgramOutputBytes  = 16 << 20
	largeContextOutputBudgetBytes  = 64 << 10
	mediumContextOutputBudgetBytes = 32 << 10
	smallContextOutputBudgetBytes  = 12 << 10
)

// ProgramOutputWindow describes how a top-level stream was windowed at the
// model boundary and where its full retained copy lives.
type ProgramOutputWindow struct {
	Stream        string `json:"stream"`
	Ref           string `json:"ref"`
	ScratchPath   string `json:"scratch_path,omitempty"`
	TotalBytes    int64  `json:"total_bytes"`
	TotalLines    int    `json:"total_lines"`
	Truncated     bool   `json:"truncated,omitempty"`
	HeadEndByte   int64  `json:"head_end_byte,omitempty"`
	HeadEndLine   int    `json:"head_end_line,omitempty"`
	TailStartByte int64  `json:"tail_start_byte,omitempty"`
	TailStartLine int    `json:"tail_start_line,omitempty"`
	OmittedBytes  int64  `json:"omitted_bytes,omitempty"`
	OmittedLines  int    `json:"omitted_lines,omitempty"`
}

// CommandOutputBudget returns the total model-boundary output budget in bytes
// for a given model context window in tokens.
func CommandOutputBudget(contextTokens int) int {
	switch {
	case contextTokens > 0 && contextTokens <= 32768:
		return smallContextOutputBudgetBytes
	case contextTokens > 32768 && contextTokens < 128000:
		return mediumContextOutputBudgetBytes
	default:
		return largeContextOutputBudgetBytes
	}
}

func splitStreamBudgets(totalBudget, stdoutBytes, stderrBytes int) (stdoutBudget, stderrBudget int) {
	switch {
	case stdoutBytes > 0 && stderrBytes > 0:
		stdoutBudget = totalBudget / 2
		stderrBudget = totalBudget - stdoutBudget
	case stdoutBytes > 0:
		stdoutBudget = totalBudget
	case stderrBytes > 0:
		stderrBudget = totalBudget
	default:
		stdoutBudget = totalBudget
		stderrBudget = totalBudget
	}
	return stdoutBudget, stderrBudget
}

// programStream captures a top-level program stream up to the retention ceiling
// and records exact source mappings from child utilities.
type programStream struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
	mappings  []toolcontract.StreamSourceMapping
}

func newProgramStream(limit int) *programStream {
	return &programStream{limit: limit}
}

func (s *programStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	origLen := len(p)
	if len(s.data)+len(p) <= s.limit {
		s.data = append(s.data, p...)
		return origLen, nil
	}
	s.truncated = true
	headKeep := s.limit / 2
	tailKeep := s.limit - headKeep
	if len(s.data) < headKeep {
		need := headKeep - len(s.data)
		s.data = append(s.data, p[:need]...)
		p = p[need:]
	}
	if len(p) >= tailKeep {
		s.data = append(s.data[:headKeep], p[len(p)-tailKeep:]...)
		return origLen, nil
	}
	curTail := len(s.data) - headKeep
	if curTail+len(p) <= tailKeep {
		s.data = append(s.data, p...)
		return origLen, nil
	}
	drop := (curTail + len(p)) - tailKeep
	copy(s.data[headKeep:], s.data[headKeep+drop:])
	s.data = append(s.data[:headKeep+curTail-drop], p...)
	return origLen, nil
}

func (s *programStream) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...)
}

func (s *programStream) Len() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.data))
}

func (s *programStream) Mappings() []toolcontract.StreamSourceMapping {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]toolcontract.StreamSourceMapping(nil), s.mappings...)
}

func (s *programStream) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.OutputEndByte <= m.OutputStartByte || m.SourceEndByte <= m.SourceStartByte {
		return nil
	}
	if n := len(s.mappings); n > 0 {
		last := &s.mappings[n-1]
		if last.Source == m.Source && last.OutputEndByte == m.OutputStartByte && last.SourceEndByte == m.SourceStartByte {
			last.OutputEndByte = m.OutputEndByte
			last.SourceEndByte = m.SourceEndByte
			return nil
		}
	}
	s.mappings = append(s.mappings, m)
	return nil
}

type childStreamView struct {
	stream     *programStream
	baseOffset int64
}

func (v *childStreamView) Write(p []byte) (int, error) {
	return v.stream.Write(p)
}

func (v *childStreamView) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	m.OutputStartByte += v.baseOffset
	m.OutputEndByte += v.baseOffset
	return v.stream.RecordSourceMapping(m)
}

func (r *ProgramRuntime) outcome(ctx context.Context, run *programRun, outputs *invocation.ProgramOutputRetention, result toolcontract.ProducerResult, stdout, stderr *programStream, scope captureprojection.Scope) (ProgramOutcome, error) {
	projection, err := r.Recorder.ProjectProgram(ctx, run.tc.Identity.SessionID, run.root.Identity.ProgramID, 4)
	if err != nil {
		return ProgramOutcome{}, err
	}
	if run.tc.Effects.Out != nil {
		receipt := projection.Program
		run.tc.Effects.Out.CommandProgram = &receipt
	}
	programID := run.root.Identity.ProgramID
	out := ProgramOutcome{
		ProgramID:        programID,
		State:            projection.Program.State,
		Status:           result.Status,
		StopCode:         result.StopCode,
		ExecutedChildren: projection.ExecutedChildren,
		CompletedEffects: projection.CompletedEffects,
		UncertainEffects: projection.UncertainEffects,
		NativeGaps:       projection.NativeGaps,
	}

	type screenedStream struct {
		name     string
		stream   *programStream
		target   *string
		refField *string
		bytes    []byte
		redacted bool
		mappings []toolcontract.StreamSourceMapping
	}
	streams := []*screenedStream{
		{name: "stdout", stream: stdout, target: &out.Stdout, refField: &out.StdoutRef},
		{name: "stderr", stream: stderr, target: &out.Stderr, refField: &out.StderrRef},
	}
	for _, s := range streams {
		raw := s.stream.Bytes()
		if len(raw) == 0 {
			continue
		}
		bound := max(int64(len(raw)), 1<<20)
		opts := captureprojection.StreamOptions{
			WorkingMemory: run.memory,
			Bounds:        secretmatch.StreamBounds{InputBytes: bound, OutputBytes: bound, MemoryBytes: 32 << 20, DetectorWork: 1 << 30},
		}
		var screened bytes.Buffer
		meta, err := r.Projector.ScreenStream(ctx, scope, "capture.native", bytes.NewReader(raw), &screened, opts)
		if err != nil {
			return ProgramOutcome{}, err
		}
		s.bytes = screened.Bytes()
		s.redacted = meta.Redacted
		if !s.redacted {
			s.mappings = r.resolveScratchMappings(ctx, run, s.stream.Mappings(), int64(len(s.bytes)))
		}
	}

	totalBudget := CommandOutputBudget(run.tc.Host.ModelContextWindow)
	stdoutBudget, stderrBudget := splitStreamBudgets(totalBudget, len(streams[0].bytes), len(streams[1].bytes))
	budgets := [2]int{stdoutBudget, stderrBudget}

	for i, s := range streams {
		if len(s.bytes) == 0 {
			continue
		}
		ref := "output:program:" + programID + ":" + s.name
		*s.refField = ref
		scratchPath := "@scratch/output/" + programID + "." + s.name
		if scratchDir := strings.TrimSpace(run.tc.Host.SessionScratchDir); scratchDir != "" {
			if err := writeReadOnlyScratchOutput(scratchDir, programID, s.name, s.bytes); err != nil {
				return ProgramOutcome{}, err
			}
		}
		if outputs != nil {
			maxSpill := max(int64(len(s.bytes)), 1<<20)
			spill, err := tooloutput.RetainScreenedStream(ctx, tooloutput.ScreenedReader(io.NopCloser(bytes.NewReader(s.bytes))), tooloutput.StreamSpillOptions{
				Store:         r.Recorder.ProgramBlobStore(r.Blobs),
				ClaimID:       programID + ":" + s.name,
				MaximumBytes:  maxSpill,
				PreviewBytes:  4 << 10,
				Admission:     r.sessionResources(scope.SessionID),
				WorkingMemory: run.memory,
				RetainedState: run.memory,
			})
			if err != nil {
				return ProgramOutcome{}, err
			}
			retained := invocation.RetainedOutput{
				Stream:   s.name,
				Reference: ref,
				Complete: !s.stream.truncated && spill.Complete,
				Redacted: s.redacted,
				Mappings: s.mappings,
			}
			if err := outputs.Attach(ctx, programID, retained, spill); err != nil {
				_ = spill.Close()
				return ProgramOutcome{}, err
			}
		}
		delivered, window, ranges, err := windowStreamOutput(ctx, s.bytes, budgets[i], s.name, ref, scratchPath)
		if err != nil {
			return ProgramOutcome{}, err
		}
		*s.target = delivered
		if window.Truncated || s.stream.truncated {
			window.Truncated = true
			out.Truncated = true
		}
		out.Outputs = append(out.Outputs, window)
		if len(s.mappings) > 0 && len(ranges) > 0 {
			r.commitStreamSourceDeliveries(ctx, run, programID, ref, s.bytes, s.mappings, ranges)
		}
	}
	return out, nil
}

func writeReadOnlyScratchOutput(scratchDir, programID, streamName string, content []byte) error {
	outDir := filepath.Join(scratchDir, "output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	target := filepath.Join(outDir, programID+"."+streamName)
	_ = os.Chmod(target, 0o600)
	_ = os.Remove(target)
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o444)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(content)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	return os.Chmod(target, 0o444)
}

func (r *ProgramRuntime) resolveScratchMappings(ctx context.Context, run *programRun, mappings []toolcontract.StreamSourceMapping, streamLen int64) []toolcontract.StreamSourceMapping {
	if len(mappings) == 0 || streamLen <= 0 {
		return nil
	}
	scratchDir := strings.TrimSpace(run.tc.Host.SessionScratchDir)
	var out []toolcontract.StreamSourceMapping
	appendCoalesced := func(m toolcontract.StreamSourceMapping) {
		if m.OutputStartByte < 0 {
			m.SourceStartByte += -m.OutputStartByte
			m.OutputStartByte = 0
		}
		if m.OutputEndByte > streamLen {
			m.SourceEndByte -= m.OutputEndByte - streamLen
			m.OutputEndByte = streamLen
		}
		if m.OutputEndByte <= m.OutputStartByte || m.SourceEndByte <= m.SourceStartByte {
			return
		}
		if m.OutputEndByte-m.OutputStartByte != m.SourceEndByte-m.SourceStartByte {
			return
		}
		if n := len(out); n > 0 {
			last := &out[n-1]
			if last.Source == m.Source && last.OutputEndByte == m.OutputStartByte && last.SourceEndByte == m.SourceStartByte {
				last.OutputEndByte = m.OutputEndByte
				last.SourceEndByte = m.SourceEndByte
				return
			}
		}
		out = append(out, m)
	}
	for _, m := range mappings {
		if prevProg, prevStream, ok := parseScratchOutputSource(m.Source.SourceID, scratchDir); ok && r.Recorder != nil {
			prevRef := "output:program:" + prevProg + ":" + prevStream
			blob, prevMappings, err := r.Recorder.ReadProgramOutputMappings(ctx, run.tc.Identity.SessionID, prevProg, prevRef)
			if err == nil && len(prevMappings) > 0 && m.Source.EndByte == blob.Size {
				proj, projErr := commandprogram.ProjectStreamMappings(ctx, run.memory, prevMappings, m.SourceStartByte, m.SourceEndByte-m.SourceStartByte, commandprogram.MappingExactSlice)
				if projErr == nil && proj != nil {
					for _, pm := range proj.Mappings {
						pm.OutputStartByte += m.OutputStartByte
						pm.OutputEndByte += m.OutputStartByte
						appendCoalesced(pm)
					}
					_ = proj.Close()
				}
			}
			continue
		}
		appendCoalesced(m)
	}
	return out
}

func parseScratchOutputSource(sourceID, scratchDir string) (programID, stream string, ok bool) {
	if !strings.HasPrefix(sourceID, "disk:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(sourceID, "disk:")
	trimmed := strings.TrimPrefix(rest, "/")
	var base string
	if strings.HasPrefix(trimmed, "@scratch/output/") {
		base = strings.TrimPrefix(trimmed, "@scratch/output/")
	} else if scratchDir != "" {
		cleanPath := filepath.Clean(rest)
		dir := filepath.Dir(cleanPath)
		wantDir := filepath.Clean(filepath.Join(scratchDir, "output"))
		canonDir := filepath.Clean(filepath.Join(fspath.CanonicalPath(scratchDir), "output"))
		if dir == wantDir || dir == canonDir {
			base = filepath.Base(cleanPath)
		}
	}
	if base == "" || strings.Contains(base, "/") || strings.Contains(base, string(filepath.Separator)) {
		return "", "", false
	}
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 {
		return "", "", false
	}
	prog, ext := base[:dot], base[dot+1:]
	if ext != "stdout" && ext != "stderr" {
		return "", "", false
	}
	return prog, ext, true
}

func (r *ProgramRuntime) sourceObserver() invocation.ProgramSourceObserver {
	if r.SourceObserver != nil {
		return r.SourceObserver
	}
	if obs, ok := r.Sessions.(invocation.ProgramSourceObserver); ok {
		return obs
	}
	return nil
}

func (r *ProgramRuntime) commitStreamSourceDeliveries(ctx context.Context, run *programRun, programID, ref string, screenedBytes []byte, mappings []toolcontract.StreamSourceMapping, ranges [][2]int64) {
	observer := r.sourceObserver()
	if observer == nil || r.Recorder == nil {
		return
	}
	responseID := run.parentReceipt
	if responseID == "" {
		responseID = programID
	}
	admission := r.sessionResources(run.tc.Identity.SessionID)
	for _, rng := range ranges {
		winStart, winEnd := rng[0], rng[1]
		if winEnd <= winStart || winEnd > int64(len(screenedBytes)) {
			continue
		}
		proj, err := commandprogram.ProjectStreamMappings(ctx, run.memory, mappings, winStart, winEnd-winStart, commandprogram.MappingExactSlice)
		if err != nil || proj == nil {
			continue
		}
		for _, m := range proj.Mappings {
			streamStart := winStart + m.OutputStartByte
			streamEnd := winStart + m.OutputEndByte
			if streamStart < 0 || streamEnd <= streamStart || streamEnd > int64(len(screenedBytes)) {
				continue
			}
			relPath, capturedBytes, ok := r.loadCapturedSourceForPin(ctx, run, m.Source)
			if !ok {
				continue
			}
			deliveredSlice := screenedBytes[streamStart:streamEnd]
			local := m
			local.OutputStartByte = 0
			local.OutputEndByte = int64(len(deliveredSlice))
			deliveryID := fmt.Sprintf("%s:%d:%d", ref, streamStart, streamEnd)
			obs, err := evidence.ConfirmDeliveredSource(relPath, m.Source, capturedBytes, deliveredSlice, local, responseID, deliveryID)
			if err != nil {
				continue
			}
			_, _ = r.Recorder.CommitProgramSourceDelivery(ctx, invocation.ProgramSourceDelivery{
				SessionID:       run.tc.Identity.SessionID,
				ProgramID:       programID,
				OutputReference: ref,
				OutputStartByte: streamStart,
				OutputEndByte:   streamEnd,
				Observation:     obs,
			}, observer, admission)
		}
		_ = proj.Close()
	}
}

func (r *ProgramRuntime) loadCapturedSourceForPin(_ context.Context, run *programRun, pin toolcontract.SourcePin) (string, []byte, bool) {
	if strings.HasPrefix(pin.SourceID, "disk:") {
		rest := strings.TrimPrefix(pin.SourceID, "disk:")
		for _, root := range run.tc.Source.Roots {
			prefix := root.ID + "/"
			if !strings.HasPrefix(rest, prefix) {
				continue
			}
			relPath := strings.TrimPrefix(rest, prefix)
			if relPath == "" {
				continue
			}
			absPath := filepath.Join(root.Path, filepath.FromSlash(relPath))
			raw, err := os.ReadFile(absPath)
			if err != nil {
				return "", nil, false
			}
			if pin.Representation == toolcontract.OriginalEncodedBytes {
				return relPath, raw, true
			}
			text, _, err := textfile.Decode(raw, textfile.LimitsForRaw(max(int64(len(raw)), 1)))
			if err != nil {
				return "", nil, false
			}
			return relPath, []byte(text), true
		}
	}
	return "", nil, false
}

func windowStreamOutput(ctx context.Context, data []byte, budget int, streamName, ref, scratchPath string) (string, ProgramOutputWindow, [][2]int64, error) {
	totalBytes := int64(len(data))
	totalLines := countLines(data)
	window := ProgramOutputWindow{
		Stream:      streamName,
		Ref:         ref,
		ScratchPath: scratchPath,
		TotalBytes:  totalBytes,
		TotalLines:  totalLines,
	}
	if budget <= 0 || len(data) <= budget {
		var ranges [][2]int64
		if totalBytes > 0 {
			ranges = [][2]int64{{0, totalBytes}}
		}
		return string(data), window, ranges, nil
	}
	headCap := budget / 2
	tailCap := budget - headCap
	headEnd := snapHeadEnd(data, headCap)
	tailStart := snapTailStart(data, headEnd, tailCap)
	if tailStart <= headEnd {
		var ranges [][2]int64
		if totalBytes > 0 {
			ranges = [][2]int64{{0, totalBytes}}
		}
		return string(data), window, ranges, nil
	}
	headEndLine := countLines(data[:headEnd])
	omittedBytes := int64(tailStart - headEnd)
	omittedLines := countLines(data[headEnd:tailStart])
	tailStartLine := headEndLine + omittedLines + 1
	nextLine := headEndLine + 1
	endLine := min(nextLine+149, max(nextLine, tailStartLine-1))

	notice, err := guidance.RenderCommandOutputOmitted(ctx, formatDecimalCommas(omittedLines), formatOmittedSize(omittedBytes), scratchPath, nextLine, endLine)
	if err != nil {
		return "", ProgramOutputWindow{}, nil, err
	}
	var b strings.Builder
	b.Grow(headEnd + len(notice) + (len(data) - tailStart) + 2)
	b.Write(data[:headEnd])
	if headEnd > 0 && data[headEnd-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString(notice)
	b.WriteByte('\n')
	b.Write(data[tailStart:])

	window.Truncated = true
	window.HeadEndByte = int64(headEnd)
	window.HeadEndLine = headEndLine
	window.TailStartByte = int64(tailStart)
	window.TailStartLine = tailStartLine
	window.OmittedBytes = omittedBytes
	window.OmittedLines = omittedLines

	var ranges [][2]int64
	if headEnd > 0 {
		ranges = append(ranges, [2]int64{0, int64(headEnd)})
	}
	if tailStart < len(data) {
		ranges = append(ranges, [2]int64{int64(tailStart), int64(len(data))})
	}
	return b.String(), window, ranges, nil
}

func snapHeadEnd(data []byte, capBytes int) int {
	if capBytes >= len(data) {
		return len(data)
	}
	if capBytes <= 0 {
		return 0
	}
	if idx := bytes.LastIndexByte(data[:capBytes], '\n'); idx >= 0 {
		return idx + 1
	}
	end := capBytes
	for end > 0 && !utf8.RuneStart(data[end]) {
		end--
	}
	if end == 0 {
		return capBytes
	}
	return end
}

func snapTailStart(data []byte, headEnd, capBytes int) int {
	start := max(len(data)-capBytes, headEnd)
	if start >= len(data) {
		return len(data)
	}
	if start > 0 && data[start-1] == '\n' {
		return start
	}
	if idx := bytes.IndexByte(data[start:], '\n'); idx >= 0 && start+idx+1 < len(data) {
		return start + idx + 1
	}
	for start < len(data) && !utf8.RuneStart(data[start]) {
		start++
	}
	return start
}

func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte{'\n'})
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

func formatDecimalCommas(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func formatOmittedSize(bytesCount int64) string {
	if bytesCount < 1024 {
		return fmt.Sprintf("%d B", bytesCount)
	}
	kb := max(int64(1), (bytesCount+512)/1024)
	return fmt.Sprintf("%s KB", formatDecimalCommas(int(kb)))
}
