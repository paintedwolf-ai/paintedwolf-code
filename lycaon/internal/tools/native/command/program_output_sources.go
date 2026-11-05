package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

func (r *ProgramRuntime) resolveOutputMappings(ctx context.Context, run *programRun, mappings []toolcontract.StreamSourceMapping, streamLen int64) *sourceMappingBuffer {
	out := newSourceMappingBuffer(ctx, run.memory)
	if len(mappings) == 0 || streamLen <= 0 {
		return out
	}
	outputDir := strings.TrimSpace(run.tc.Host.SessionOutputDir)
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
		out.Append(m)
	}
	for _, m := range mappings {
		if out.disabled {
			break
		}
		if prevProg, prevStream, ok := parseOutputSource(m.Source.SourceID, outputDir); ok && r.Recorder != nil {
			prevRef := "output:program:" + prevProg + ":" + prevStream
			previous, err := r.Recorder.ReadProgramOutputMappings(ctx, run.tc.Identity.SessionID, prevProg, prevRef, run.memory)
			if err == nil && len(previous.Mappings) > 0 && m.Source.EndByte == previous.Blob.Size && previous.Blob.MatchesContentDigest(m.Source.SHA256) {
				proj, projErr := commandprogram.ProjectStreamMappings(ctx, run.memory, previous.Mappings, m.SourceStartByte, m.SourceEndByte-m.SourceStartByte, commandprogram.MappingExactSlice)
				if projErr == nil && proj != nil {
					for _, pm := range proj.Mappings {
						pm.OutputStartByte += m.OutputStartByte
						pm.OutputEndByte += m.OutputStartByte
						if m.PrintedLine != 0 {
							pm.PrintedLine = m.PrintedLine
						}
						appendCoalesced(pm)
					}
					_ = proj.Close()
				}
			}
			if previous != nil {
				previous.Close()
			}
			continue
		}
		appendCoalesced(m)
	}
	return out
}

func parseOutputSource(sourceID, outputDir string) (programID, stream string, ok bool) {
	if !strings.HasPrefix(sourceID, "disk:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(sourceID, "disk:")
	trimmed := strings.TrimPrefix(rest, "/")
	var base string
	if strings.HasPrefix(trimmed, "@output/") {
		base = strings.TrimPrefix(trimmed, "@output/")
	} else if outputDir != "" {
		cleanPath := filepath.Clean(rest)
		dir := filepath.Dir(cleanPath)
		wantDir := filepath.Clean(outputDir)
		canonDir := filepath.Clean(fspath.CanonicalPath(outputDir))
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
	responseID := run.parentReceipt
	if responseID == "" {
		responseID = programID
	}
	for _, rng := range ranges {
		winStart, winEnd := rng[0], rng[1]
		if winEnd <= winStart || winEnd-winStart > int64(len(screenedBytes)) {
			continue
		}
		proj, err := commandprogram.ProjectStreamMappings(ctx, run.memory, mappings, winStart, winEnd-winStart, commandprogram.MappingExactSlice)
		if err != nil || proj == nil {
			continue
		}
		for _, group := range groupNumberedDeliveries(proj.Mappings) {
			var lines []evidence.DeliveredLine
			for _, m := range group {
				streamStart, streamEnd := winStart+m.OutputStartByte, winStart+m.OutputEndByte
				if streamStart < winStart || streamEnd <= streamStart || streamEnd-winStart > int64(len(screenedBytes)) {
					lines = nil
					break
				}
				delivered := screenedBytes[streamStart-winStart : streamEnd-winStart]
				tools.RememberCommandSourceDelivery(ctx, run.tc, run.memory, m, delivered)
				local := m
				local.OutputStartByte, local.OutputEndByte = streamStart, streamEnd
				lines = append(lines, evidence.DeliveredLine{Mapping: local, Bytes: delivered})
			}
			if len(lines) == 0 || observer == nil || r.Recorder == nil {
				continue
			}
			r.commitSourceDelivery(ctx, run, observer, programID, ref, responseID, lines)
		}
		_ = proj.Close()
	}
}

// groupNumberedDeliveries joins consecutive numbered lines of one source. Every other
// mapping stands alone.
func groupNumberedDeliveries(mappings []toolcontract.StreamSourceMapping) [][]toolcontract.StreamSourceMapping {
	var groups [][]toolcontract.StreamSourceMapping
	for _, m := range mappings {
		if n := len(groups); n > 0 {
			last := groups[n-1][len(groups[n-1])-1]
			if m.PrintedLine != 0 && last.PrintedLine+1 == m.PrintedLine && last.Source == m.Source && last.SourceEndByte == m.SourceStartByte {
				groups[n-1] = append(groups[n-1], m)
				continue
			}
		}
		groups = append(groups, []toolcontract.StreamSourceMapping{m})
	}
	return groups
}

// commitSourceDelivery records one delivered window as one observation.
func (r *ProgramRuntime) commitSourceDelivery(ctx context.Context, run *programRun, observer invocation.ProgramSourceObserver, programID, ref, responseID string, lines []evidence.DeliveredLine) {
	first, last := lines[0].Mapping, lines[len(lines)-1].Mapping
	var deliveredBytes int
	for _, line := range lines {
		deliveredBytes += len(line.Bytes)
	}
	relPath, captured, release, ok := r.loadCapturedSourceForPin(ctx, run, first.Source, deliveredBytes)
	if !ok {
		return
	}
	defer release()
	deliveryID := fmt.Sprintf("%s:%d:%d", ref, first.OutputStartByte, last.OutputEndByte)
	var obs evidence.DeliveredSourceObservation
	var err error
	if first.PrintedLine != 0 {
		obs, err = evidence.ConfirmDeliveredLines(relPath, first.Source, captured, lines, responseID, deliveryID)
	} else {
		local := first
		local.OutputStartByte, local.OutputEndByte = 0, int64(deliveredBytes)
		obs, err = evidence.ConfirmDeliveredSource(relPath, first.Source, captured, lines[0].Bytes, local, responseID, deliveryID)
	}
	if err != nil {
		return
	}
	delivery := invocation.ProgramSourceDelivery{SessionID: run.tc.Identity.SessionID, ProgramID: programID, OutputReference: ref, Observation: obs,
		OutputStartByte: first.OutputStartByte, OutputEndByte: last.OutputEndByte}
	if first.PrintedLine != 0 {
		for _, line := range lines {
			delivery.NumberedLines = append(delivery.NumberedLines, line.Mapping)
		}
	}
	settlement, commitErr := r.Recorder.CommitProgramSourceDelivery(ctx, delivery, observer, r.sessionResources(run.tc.Identity.SessionID))
	handle := strings.TrimSpace(settlement.SourceOwnerReference)
	if commitErr != nil || handle == "" || run.tc.Effects.Out == nil {
		return
	}
	for _, seen := range run.tc.Effects.Out.SourceDeliveries {
		if seen.Handle == handle {
			return
		}
	}
	if len(run.tc.Effects.Out.SourceDeliveries) < maxPrintedDeliveryTags {
		rec := obs.Record()
		fact := rec.SourceDelivery
		run.tc.Effects.Out.SourceDeliveries = append(run.tc.Effects.Out.SourceDeliveries, tools.SourceDeliveryTag{
			Handle: handle, Coverage: hostmarker.FormatEvidenceCoverage(relPath, fact.StartLine, fact.EndLine),
		})
	}
}

// maxPrintedDeliveryTags bounds a result's tags. A wide search would otherwise print
// one for every match; its records stay citable by path.
const maxPrintedDeliveryTags = 16

// Capturing source for optional read credit never makes a successful output fail.
// Admission covers the raw buffer, decoder expansion/copies, and delivered lines.
func (r *ProgramRuntime) loadCapturedSourceForPin(ctx context.Context, run *programRun, pin toolcontract.SourcePin, deliveredBytes int) (string, []byte, func(), bool) {
	if !strings.HasPrefix(pin.SourceID, "disk:") || deliveredBytes < 1 || int64(deliveredBytes) > (math.MaxInt64-(64<<10))/32 || run.memory == nil {
		return "", nil, nil, false
	}
	rest := strings.TrimPrefix(pin.SourceID, "disk:")
	var rootPath, relPath string
	for _, root := range run.tc.Source.Roots {
		prefix := root.ID + "/"
		if !strings.HasPrefix(rest, prefix) {
			continue
		}
		if candidate := strings.TrimPrefix(rest, prefix); candidate != "" {
			rootPath, relPath = root.Path, candidate
			break
		}
	}
	if relPath == "" {
		return "", nil, nil, false
	}
	file, err := fseffect.OpenRead(fseffect.Location{Root: rootPath, Rel: filepath.FromSlash(relPath)})
	if err != nil {
		return "", nil, nil, false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	overhead := int64(deliveredBytes)*32 + (64 << 10)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > (math.MaxInt64-overhead)/8-1 {
		return "", nil, nil, false
	}
	size := info.Size()
	release, err := run.memory.Reserve(ctx, 8*(size+1)+overhead)
	if err != nil {
		return "", nil, nil, false
	}
	keep := false
	defer func() {
		if !keep {
			release()
		}
	}()
	raw := make([]byte, size+1)
	n, err := io.ReadFull(io.LimitReader(file, size+1), raw)
	if int64(n) != size || (!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF)) {
		return "", nil, nil, false
	}
	raw = raw[:n]
	if pin.Representation == toolcontract.OriginalEncodedBytes {
		keep = true
		return relPath, raw, release, true
	}
	text, _, err := textfile.Decode(raw, textfile.LimitsForRaw(max(size, 1)))
	if err != nil {
		return "", nil, nil, false
	}
	keep = true
	return relPath, []byte(text), release, true
}
