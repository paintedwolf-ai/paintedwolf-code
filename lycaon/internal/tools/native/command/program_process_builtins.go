package command

import (
	"context"
	"fmt"
	"os"
	"regexp"
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
		visible, _ := hostprocess.VisibleProcesses(ctx)
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

		var out strings.Builder
		if formatOpt, ok := bound.Last("-o"); ok && len(formatOpt.Values) > 0 {
			writePsCustomFormat(&out, formatOpt.Values[0].Value, visible, statuses, ownedByPID, jobs)
		} else if bsdAux {
			out.WriteString("USER PID %CPU %MEM VSZ RSS TTY STAT START TIME COMMAND\n")
			seenHandles := map[string]bool{}
			for _, proc := range visible {
				cmdText := proc.Command
				userText := strconv.FormatUint(uint64(proc.UID), 10)
				if job, owned := ownedByPID[proc.PID]; owned {
					seenHandles[job.Handle] = true
					if job.CommandLine != "" {
						cmdText = job.CommandLine
					}
					fmt.Fprintf(&out, "%s %d 0.0 0.0 0 0 ? S - 0:00 %s [session job #%d %s running]\n", userText, proc.PID, cmdText, jobs.Number(job.Handle), job.Handle)
				} else {
					fmt.Fprintf(&out, "%s %d 0.0 0.0 0 0 ? S - 0:00 %s\n", userText, proc.PID, cmdText)
				}
			}
			for _, job := range statuses {
				if seenHandles[job.Handle] {
					continue
				}
				fmt.Fprintf(&out, "session %d 0.0 0.0 0 0 ? R - 0:00 [%d] %s running %s [session job]\n", job.PID, jobs.Number(job.Handle), job.Handle, job.CommandLine)
			}
		} else if fullFmt {
			out.WriteString("UID PID PPID C STIME TTY TIME CMD\n")
			seenHandles := map[string]bool{}
			for _, proc := range visible {
				cmdText := proc.Command
				userText := strconv.FormatUint(uint64(proc.UID), 10)
				if job, owned := ownedByPID[proc.PID]; owned {
					seenHandles[job.Handle] = true
					if job.CommandLine != "" {
						cmdText = job.CommandLine
					}
					fmt.Fprintf(&out, "%s %d %d 0 - ? 00:00:00 %s [session job #%d %s running]\n", userText, proc.PID, proc.ParentPID, cmdText, jobs.Number(job.Handle), job.Handle)
				} else {
					fmt.Fprintf(&out, "%s %d %d 0 - ? 00:00:00 %s\n", userText, proc.PID, proc.ParentPID, cmdText)
				}
			}
			for _, job := range statuses {
				if seenHandles[job.Handle] {
					continue
				}
				fmt.Fprintf(&out, "session %d 0 0 - ? 00:00:00 [%d] %s running %s [session job]\n", job.PID, jobs.Number(job.Handle), job.Handle, job.CommandLine)
			}
		} else {
			out.WriteString("PID TTY TIME CMD\n")
			seenHandles := map[string]bool{}
			for _, proc := range visible {
				cmdText := proc.Command
				if job, owned := ownedByPID[proc.PID]; owned {
					seenHandles[job.Handle] = true
					if job.CommandLine != "" {
						cmdText = job.CommandLine
					}
					fmt.Fprintf(&out, "%d ? 00:00:00 %s [session job #%d %s running]\n", proc.PID, cmdText, jobs.Number(job.Handle), job.Handle)
				} else {
					fmt.Fprintf(&out, "%d ? 00:00:00 %s\n", proc.PID, cmdText)
				}
			}
			for _, job := range statuses {
				if seenHandles[job.Handle] {
					continue
				}
				fmt.Fprintf(&out, "[%d] %s running %s [session job]\n", jobs.Number(job.Handle), job.Handle, job.CommandLine)
			}
		}
		if _, err := fmt.Fprint(nonNilWriter(streams.Output), out.String()); err != nil {
			return ownerFailure(err), err
		}
		return readResult(0), nil
	})
}

func writePsCustomFormat(out *strings.Builder, spec string, visible []hostprocess.VisibleProcess, statuses []jobStatus, ownedByPID map[int]jobStatus, jobs *SessionJobs) {
	rawCols := strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ' ' })
	if len(rawCols) == 0 {
		return
	}
	showHeader := true
	cols := make([]string, 0, len(rawCols))
	headers := make([]string, 0, len(rawCols))
	for _, raw := range rawCols {
		if strings.HasSuffix(raw, "=") {
			showHeader = false
			raw = strings.TrimSuffix(raw, "=")
		}
		col := strings.ToLower(strings.TrimSpace(raw))
		cols = append(cols, col)
		headers = append(headers, strings.ToUpper(col))
	}
	if showHeader {
		out.WriteString(strings.Join(headers, " ") + "\n")
	}
	seenHandles := map[string]bool{}
	for _, proc := range visible {
		job, owned := ownedByPID[proc.PID]
		if owned {
			seenHandles[job.Handle] = true
		}
		fields := make([]string, 0, len(cols))
		for _, col := range cols {
			switch col {
			case "pid":
				fields = append(fields, strconv.Itoa(proc.PID))
			case "ppid":
				fields = append(fields, strconv.Itoa(proc.ParentPID))
			case "pgid":
				fields = append(fields, strconv.Itoa(proc.PID))
			case "user", "uid":
				fields = append(fields, strconv.FormatUint(uint64(proc.UID), 10))
			case "stat", "state", "s":
				fields = append(fields, "S")
			case "comm":
				fields = append(fields, proc.Name)
			case "command", "args", "cmd":
				cmdText := proc.Command
				if owned {
					cmdText += fmt.Sprintf(" [session job #%d %s running]", jobs.Number(job.Handle), job.Handle)
				}
				fields = append(fields, cmdText)
			default:
				fields = append(fields, "0")
			}
		}
		out.WriteString(strings.Join(fields, " ") + "\n")
	}
	for _, job := range statuses {
		if seenHandles[job.Handle] {
			continue
		}
		fields := make([]string, 0, len(cols))
		for _, col := range cols {
			switch col {
			case "pid":
				if job.PID > 0 {
					fields = append(fields, strconv.Itoa(job.PID))
				} else {
					fields = append(fields, job.Handle)
				}
			case "user", "uid":
				fields = append(fields, "session")
			case "stat", "state", "s":
				fields = append(fields, "R")
			case "comm":
				fields = append(fields, firstCommandWord(job.CommandLine))
			case "command", "args", "cmd":
				fields = append(fields, fmt.Sprintf("[%d] %s running %s [session job]", jobs.Number(job.Handle), job.Handle, job.CommandLine))
			default:
				fields = append(fields, "0")
			}
		}
		out.WriteString(strings.Join(fields, " ") + "\n")
	}
}

type procCandidate struct {
	id     string
	pid    int
	handle string
	comm   string
	cmd    string
	owned  bool
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
	visible, _ := hostprocess.VisibleProcesses(ctx)
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
		cmdLine := proc.Command
		comm := proc.Name
		if owned {
			seenHandles[job.Handle] = true
			if job.CommandLine != "" {
				cmdLine = job.CommandLine
				comm = firstCommandWord(job.CommandLine)
			}
		}
		subject := comm
		if fullMatch {
			subject = cmdLine
		}
		if re.MatchString(subject) || (!fullMatch && re.MatchString(proc.Name)) {
			matches = append(matches, procCandidate{
				id:     strconv.Itoa(proc.PID),
				pid:    proc.PID,
				handle: job.Handle,
				comm:   comm,
				cmd:    cmdLine,
				owned:  owned,
			})
		}
	}
	for _, job := range statuses {
		if seenHandles[job.Handle] {
			continue
		}
		comm := firstCommandWord(job.CommandLine)
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

func firstCommandWord(commandLine string) string {
	fields := strings.Fields(commandLine)
	if len(fields) == 0 {
		return ""
	}
	if len(fields) >= 3 && fields[0] == "external" && fields[1] == "--" {
		fields = fields[2:]
	}
	word := fields[0]
	if idx := strings.LastIndexByte(word, '/'); idx >= 0 {
		word = word[idx+1:]
	}
	return word
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
			return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "pgrep", "pattern": operands[0], "word": operands[0], "reason": err.Error()})
		}
		if len(matches) == 0 {
			return readResult(1), nil
		}
		if newest {
			matches = matches[len(matches)-1:]
		} else if oldest {
			matches = matches[:1]
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
			return ownerFailure(err), err
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
		if newest {
			owned = owned[len(owned)-1:]
		} else if oldest {
			owned = owned[:1]
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
