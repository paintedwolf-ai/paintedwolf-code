package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func psBuiltin(jobs *SessionJobs, spec toolcontract.CommandSpecification) commandprogram.OperationDispatcher {
	return jobBuiltin(func(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		bound, err := (commandprogram.CatalogBinder{}).Bind(spec, request.Argv)
		if err != nil {
			return refusedResult("COMMAND_OPERAND_INVALID"), argvRejection(spec, err)
		}
		statuses, err := jobs.List(ctx)
		if err != nil {
			return ownerFailure(err), err
		}
		visible, err := hostprocess.VisibleProcesses(ctx)
		if err != nil {
			return ownerFailure(err), err
		}
		ownedByPID := map[int]jobStatus{}
		for _, job := range statuses {
			if job.PID > 0 {
				ownedByPID[job.PID] = job
			}
		}
		bsdAux := len(bound.Options("-u")) > 0
		fullFmt := len(bound.Options("-f")) > 0
		var targets []string
		for _, opt := range bound.Options("-p") {
			if len(opt.Values) > 0 {
				for _, part := range strings.Split(opt.Values[0].Value, ",") {
					if part = strings.TrimSpace(part); part != "" {
						targets = append(targets, part)
					}
				}
			}
		}
		for _, op := range bound.Operands() {
			if strings.TrimLeft(op.Value, "auxef") == "" {
				if strings.ContainsRune(op.Value, 'u') {
					bsdAux = true
				}
				if strings.ContainsRune(op.Value, 'f') {
					fullFmt = true
				}
				continue
			}
			targets = append(targets, op.Value)
		}
		if len(targets) > 0 {
			wantedHandles := map[string]bool{}
			wantedPIDs := map[int]bool{}
			byPID := map[int]hostprocess.VisibleProcess{}
			for _, proc := range visible {
				byPID[proc.PID] = proc
			}
			for _, target := range targets {
				if handle, resolveErr := jobs.Resolve(target); resolveErr == nil {
					wantedHandles[handle] = true
					if pid := jobs.pid(handle); pid > 0 {
						wantedPIDs[pid] = true
					}
					continue
				}
				if pid, convErr := strconv.Atoi(target); convErr == nil && pid > 0 {
					if _, ok := byPID[pid]; ok {
						wantedPIDs[pid] = true
						continue
					}
				}
				_, _ = fmt.Fprintf(nonNilWriter(streams.Error), "ps: %s: no matching process\n", target)
			}
			filteredJobs := statuses[:0]
			for _, job := range statuses {
				if wantedHandles[job.Handle] || (job.PID > 0 && wantedPIDs[job.PID]) {
					filteredJobs = append(filteredJobs, job)
				}
			}
			statuses = filteredJobs
			filteredProcs := visible[:0]
			for _, proc := range visible {
				if wantedPIDs[proc.PID] {
					filteredProcs = append(filteredProcs, proc)
				}
			}
			visible = filteredProcs
		}

		columns := "pid,tty,time,cmd"
		if bsdAux {
			columns = "user,pid,%cpu,%mem,vsz,rss,tty,stat,start,time,command"
		}
		if fullFmt && !bsdAux {
			columns = "uid,pid,ppid,c,stime,tty,time,cmd"
		}
		if options := bound.Options("-o"); len(options) > 0 {
			var formats []string
			for _, option := range options {
				for _, value := range option.Values {
					formats = append(formats, value.Value)
				}
			}
			columns = strings.Join(formats, ",")
		}
		parsed, err := parsePsColumns(columns)
		if err != nil {
			if _, writeErr := fmt.Fprintln(nonNilWriter(streams.Error), err); writeErr != nil {
				return ownerFailure(writeErr), writeErr
			}
			return readResult(1), nil
		}
		var out strings.Builder
		writePsCustomFormat(&out, parsed, visible, statuses, ownedByPID, jobs)
		if _, err := fmt.Fprint(nonNilWriter(streams.Output), out.String()); err != nil {
			return readFailure(err), err
		}
		return readResult(0), nil
	})
}

type procCandidate struct {
	id      string
	pid     int
	handle  string
	comm    string
	cmd     string
	owned   bool
	started uint64
}

func matchProcessCandidates(ctx context.Context, jobs *SessionJobs, pattern string, fullMatch, exactMatch bool) ([]procCandidate, error) {
	expr := pattern
	if exactMatch {
		expr = "^(?:" + pattern + ")$"
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, err
	}
	statuses, err := jobs.List(ctx)
	if err != nil {
		return nil, err
	}
	visible, err := hostprocess.VisibleProcesses(ctx)
	if err != nil {
		return nil, err
	}
	ownedByPID := map[int]jobStatus{}
	for _, job := range statuses {
		if job.PID > 0 {
			ownedByPID[job.PID] = job
		}
	}
	var matches []procCandidate
	seenHandles := map[string]bool{}
	selfPID := os.Getpid()
	for _, proc := range visible {
		if proc.PID == selfPID {
			continue
		}
		job, owned := ownedByPID[proc.PID]
		owned = owned && job.Process.Instance == proc.Instance
		cmdLine := proc.Command
		comm := proc.Name
		if owned {
			seenHandles[job.Handle] = true
			if job.CommandLine != "" {
				cmdLine = job.CommandLine
				comm = job.Name
			}
		}
		subject := comm
		if fullMatch {
			subject = cmdLine
		}
		if re.MatchString(subject) || (!fullMatch && re.MatchString(proc.Name)) {
			matches = append(matches, procCandidate{
				id:      strconv.Itoa(proc.PID),
				pid:     proc.PID,
				handle:  job.Handle,
				comm:    comm,
				cmd:     cmdLine,
				owned:   owned,
				started: proc.StartOrder,
			})
		}
	}
	for _, job := range statuses {
		if seenHandles[job.Handle] || job.PID > 0 {
			continue
		}
		comm := job.Name
		subject := comm
		if fullMatch {
			subject = job.CommandLine
		}
		if re.MatchString(subject) {
			id := job.Handle
			if job.PID > 0 {
				id = strconv.Itoa(job.PID)
			}
			matches = append(matches, procCandidate{
				id:     id,
				pid:    job.PID,
				handle: job.Handle,
				comm:   comm,
				cmd:    job.CommandLine,
				owned:  true,
			})
		}
	}
	return matches, nil
}

func pgrepBuiltin(jobs *SessionJobs, spec toolcontract.CommandSpecification) commandprogram.OperationDispatcher {
	return jobBuiltin(func(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		bound, err := (commandprogram.CatalogBinder{}).Bind(spec, request.Argv)
		if err != nil {
			return refusedResult("COMMAND_OPERAND_INVALID"), argvRejection(spec, err)
		}
		operands := bound.Operands().Values()
		if len(operands) != 1 || operands[0] == "" {
			return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "pgrep", "reason": "requires one pattern operand"})
		}
		fullMatch := len(bound.Options("-f")) > 0
		exactMatch := len(bound.Options("-x")) > 0
		listName := len(bound.Options("-l")) > 0
		listFull := len(bound.Options("-a")) > 0
		newest := len(bound.Options("-n")) > 0
		oldest := len(bound.Options("-o")) > 0

		matches, err := matchProcessCandidates(ctx, jobs, operands[0], fullMatch, exactMatch)
		if err != nil {
			var syntaxErr *syntax.Error
			if !errors.As(err, &syntaxErr) {
				return ownerFailure(err), err
			}
			return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "pgrep", "pattern": operands[0], "word": operands[0], "reason": err.Error()})
		}
		if newest || oldest {
			matches = selectProcessAge(matches, newest)
		}
		if len(matches) == 0 {
			return readResult(1), nil
		}
		var out strings.Builder
		for _, m := range matches {
			switch {
			case listFull:
				if m.owned {
					fmt.Fprintf(&out, "%s %s [session job]\n", m.id, m.cmd)
				} else {
					fmt.Fprintf(&out, "%s %s\n", m.id, m.cmd)
				}
			case listName:
				if m.owned {
					fmt.Fprintf(&out, "%s %s [session job]\n", m.id, m.comm)
				} else {
					fmt.Fprintf(&out, "%s %s\n", m.id, m.comm)
				}
			default:
				fmt.Fprintf(&out, "%s\n", m.id)
			}
		}
		if _, err := fmt.Fprint(nonNilWriter(streams.Output), out.String()); err != nil {
			return readFailure(err), err
		}
		return readResult(0), nil
	})
}

func pkillBuiltin(jobs *SessionJobs, spec toolcontract.CommandSpecification) commandprogram.OperationDispatcher {
	return jobBuiltin(func(ctx context.Context, request commandprogram.OperationRequest, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
		bound, err := (commandprogram.CatalogBinder{}).Bind(spec, request.Argv)
		if err != nil {
			return refusedResult("COMMAND_OPERAND_INVALID"), argvRejection(spec, err)
		}
		signal := "TERM"
		if option, ok := bound.Last("-s"); ok && len(option.Values) > 0 {
			signal = strings.TrimPrefix(strings.ToUpper(option.Values[0].Value), "SIG")
		}
		operands := bound.Operands().Values()
		if len(operands) != 1 || operands[0] == "" {
			return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "pkill", "reason": "requires one pattern operand"})
		}
		fullMatch := len(bound.Options("-f")) > 0
		exactMatch := len(bound.Options("-x")) > 0
		newest := len(bound.Options("-n")) > 0
		oldest := len(bound.Options("-o")) > 0

		matches, err := matchProcessCandidates(ctx, jobs, operands[0], fullMatch, exactMatch)
		if err != nil {
			var syntaxErr *syntax.Error
			if !errors.As(err, &syntaxErr) {
				return ownerFailure(err), err
			}
			return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "pkill", "pattern": operands[0], "word": operands[0], "reason": err.Error()})
		}
		var owned, foreign []procCandidate
		for _, m := range matches {
			if m.owned {
				owned = append(owned, m)
			} else {
				foreign = append(foreign, m)
			}
		}
		if len(owned) == 0 {
			if len(foreign) > 0 {
				reject := processNotOwned("pkill", foreign[0].id)
				return ownerFailure(reject), reject
			}
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: false, EffectCertainty: toolcontract.NoEffect}, nil
		}
		if newest || oldest {
			owned = selectProcessAge(owned, newest)
		}
		if len(owned) == 0 {
			return readResult(1), nil
		}
		for _, m := range owned {
			ref := m.handle
			if ref == "" {
				ref = m.id
			}
			if err := jobs.Signal(ctx, ref, signal); err != nil {
				if reject := toolrejection.AsToolReject(err); reject != nil && reject.Data != nil {
					reject.Data["command"] = "pkill"
				}
				return ownerFailure(err), err
			}
		}
		return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectAttempted}, nil
	})
}

func selectProcessAge(matches []procCandidate, newest bool) []procCandidate {
	known := slices.DeleteFunc(slices.Clone(matches), func(p procCandidate) bool { return p.started == 0 })
	if len(known) == 0 {
		return nil
	}
	slices.SortStableFunc(known, func(a, b procCandidate) int {
		if a.started < b.started {
			return -1
		}
		if a.started > b.started {
			return 1
		}
		return a.pid - b.pid
	})
	if newest {
		return known[len(known)-1:]
	}
	return known[:1]
}
