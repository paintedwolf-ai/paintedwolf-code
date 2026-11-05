package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
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
		for _, m := range proj.Mappings {
			streamStart := winStart + m.OutputStartByte
			streamEnd := winStart + m.OutputEndByte
			if streamStart < winStart || streamEnd <= streamStart || streamEnd-winStart > int64(len(screenedBytes)) {
				continue
			}
			deliveredSlice := screenedBytes[streamStart-winStart : streamEnd-winStart]
			tools.RememberCommandSourceDelivery(ctx, run.tc, run.memory, m, deliveredSlice)
			if observer == nil || r.Recorder == nil {
				continue
			}
			relPath, capturedBytes, release, ok := r.loadCapturedSourceForPin(ctx, run, m.Source, len(deliveredSlice))
			if !ok {
				continue
			}
			local := m
			local.OutputStartByte = 0
			local.OutputEndByte = int64(len(deliveredSlice))
			deliveryID := fmt.Sprintf("%s:%d:%d", ref, streamStart, streamEnd)
			obs, err := evidence.ConfirmDeliveredSource(relPath, m.Source, capturedBytes, deliveredSlice, local, responseID, deliveryID)
			if err != nil {
				release()
				continue
			}
			tools.RememberCommandSourceDelivery(ctx, run.tc, run.memory, local, deliveredSlice)
			settlement, commitErr := r.Recorder.CommitProgramSourceDelivery(ctx, invocation.ProgramSourceDelivery{
				SessionID:       run.tc.Identity.SessionID,
				ProgramID:       programID,
				OutputReference: ref,
				OutputStartByte: streamStart,
				OutputEndByte:   streamEnd,
				Observation:     obs,
			}, observer, r.sessionResources(run.tc.Identity.SessionID))
			if commitErr == nil && run.tc.Effects.Out != nil {
				if handle := strings.TrimSpace(settlement.SourceOwnerReference); handle != "" && !slices.Contains(run.tc.Effects.Out.SourceDeliveryHandles, handle) {
					run.tc.Effects.Out.SourceDeliveryHandles = append(run.tc.Effects.Out.SourceDeliveryHandles, handle)
				}
			}
			release()
		}
		_ = proj.Close()
	}
}

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
