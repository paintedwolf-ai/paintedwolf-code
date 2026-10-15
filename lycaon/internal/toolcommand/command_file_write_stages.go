package toolcommand

import (
	"context"
	"path/filepath"
	"strings"
)

func analyzeStatementFileWrite(
	ctx context.Context,
	stmt bashStatement,
	stdinText string,
	hasStdin bool,
	stdoutTo string,
	appendFlag bool,
	depth int,
) (matched bool, path string, appendMode bool, calls []ReplacementCall, exact bool) {
	if len(stmt.stages) == 0 {
		return false, "", false, nil, false
	}
	if len(stmt.stages) == 1 {
		return analyzeSingleStageFileWrite(ctx, stmt.stages[0], stdinText, hasStdin, stdoutTo, appendFlag, depth)
	}
	if len(stmt.stages) == 2 {
		return analyzeTwoStagePipelineFileWrite(ctx, stmt.stages[0], stmt.stages[1], stdinText, hasStdin, stdoutTo, appendFlag, depth)
	}
	first, last := stmt.stages[0], stmt.stages[len(stmt.stages)-1]
	if _, _, hasStream := stageInputStream(first, stdinText, hasStdin); !hasStream {
		return false, "", false, nil, false
	}
	if sinkPath, sinkApp, _, ok := stageOutputSink(last, stdoutTo, appendFlag); ok {
		return true, sinkPath, sinkApp, nil, false
	}
	return false, "", false, nil, false
}

func analyzeSingleStageFileWrite(
	ctx context.Context,
	st bashStage,
	stdinText string,
	hasStdin bool,
	stdoutTo string,
	appendFlag bool,
	depth int,
) (matched bool, path string, appendMode bool, calls []ReplacementCall, exact bool) {
	outPath, outLiteral, outAppend, outConflicts := stageStdoutRedirect(st.redirects, stdoutTo, appendFlag)
	streamBody, streamLiteral, hasStream := stageInputStream(st, stdinText, hasStdin)

	if st.subshell != "" && len(st.argv) == 0 {
		return analyzeScriptFileWrites(ctx, st.subshell, streamBody, hasStream, outPath, outAppend, depth+1)
	}
	if innerScript, isShell := unwrapShellCommand(st); isShell {
		m, p, a, c, ex := analyzeScriptFileWrites(ctx, innerScript, streamBody, hasStream, outPath, outAppend, depth+1)
		if m {
			if outConflicts || len(st.env) > 0 || hasInputOrStderrRedirect(st.redirects) {
				return true, p, a, nil, false
			}
			return true, p, a, c, ex
		}
		if hasStream && outPath != "" {
			return true, outPath, outAppend, nil, false
		}
		return false, "", false, nil, false
	}
	if isShellProgram(st.argv) && len(st.heredocs) == 1 && outPath == "" {
		m, p, a, c, ex := analyzeScriptFileWrites(ctx, st.heredocs[0].body, "", false, "", false, depth+1)
		if m {
			if !st.heredocs[0].literal || len(st.env) > 0 || hasInputOrStderrRedirect(st.redirects) {
				return true, p, a, nil, false
			}
			return true, p, a, c, ex
		}
	}

	if depth > 0 && !hasStream && outPath != "" && !outConflicts && !hasInputOrStderrRedirect(st.redirects) {
		if body, bodyLit, ok := literalEchoOrPrintf(st.argv); ok {
			if len(st.env) == 0 && outLiteral && bodyLit && isRepresentableWriteTarget(outPath) {
				return true, outPath, outAppend, []ReplacementCall{buildWriteCall(outPath, body, outAppend)}, true
			}
			return true, outPath, outAppend, nil, false
		}
	}

	if !hasStream {
		return false, "", false, nil, false
	}

	sinkPath, sinkApp, sinkExact, hasSink := stageOutputSink(st, stdoutTo, appendFlag)
	if !hasSink {
		return false, "", false, nil, false
	}

	hasInlineHeredoc := len(st.heredocs) > 0 || len(st.herestrings) > 0
	isPassThrough, passExact := isPassThroughWriterStage(st, outPath != "")
	if !hasInlineHeredoc && !isPassThrough {
		return false, "", false, nil, false
	}

	canExact := isPassThrough && passExact && sinkExact && streamLiteral && !outConflicts &&
		len(st.env) == 0 && !hasInputOrStderrRedirect(st.redirects) && isRepresentableWriteTarget(sinkPath)
	if canExact {
		return true, sinkPath, sinkApp, []ReplacementCall{buildWriteCall(sinkPath, streamBody, sinkApp)}, true
	}
	return true, sinkPath, sinkApp, nil, false
}

func analyzeTwoStagePipelineFileWrite(
	_ context.Context,
	producer, consumer bashStage,
	stdinText string,
	hasStdin bool,
	stdoutTo string,
	appendFlag bool,
	depth int,
) (matched bool, path string, appendMode bool, calls []ReplacementCall, exact bool) {
	streamBody, streamLiteral, hasStream := stageInputStream(producer, stdinText, hasStdin)
	producerPassThrough := false
	producerExact := false

	if hasStream {
		outPath, _, _, outConflicts := stageStdoutRedirect(producer.redirects, "", false)
		if outPath == "" && !outConflicts {
			producerPassThrough, producerExact = isPassThroughProducer(producer)
		}
	} else if depth > 0 {
		if body, lit, ok := literalEchoOrPrintf(producer.argv); ok && len(producer.redirects) == 0 {
			hasStream = true
			streamBody = body
			streamLiteral = lit
			producerPassThrough = true
			producerExact = lit && len(producer.env) == 0
		}
	}

	if !hasStream {
		return false, "", false, nil, false
	}
	hasInlineHeredoc := len(producer.heredocs) > 0 || len(producer.herestrings) > 0
	if !hasInlineHeredoc && !producerPassThrough {
		return false, "", false, nil, false
	}

	sinkPath, sinkApp, consumerExact, ok := consumerSinkStage(consumer, stdoutTo, appendFlag)
	if !ok {
		return false, "", false, nil, false
	}

	canExact := producerPassThrough && producerExact && consumerExact && streamLiteral &&
		len(producer.env) == 0 && len(consumer.env) == 0 &&
		!hasInputOrStderrRedirect(producer.redirects) && !hasInputOrStderrRedirect(consumer.redirects) &&
		isRepresentableWriteTarget(sinkPath)
	if canExact {
		return true, sinkPath, sinkApp, []ReplacementCall{buildWriteCall(sinkPath, streamBody, sinkApp)}, true
	}
	return true, sinkPath, sinkApp, nil, false
}

func stageInputStream(st bashStage, stdinText string, hasStdin bool) (body string, literal, ok bool) {
	if len(st.heredocs) > 0 {
		if len(st.heredocs) == 1 && len(st.herestrings) == 0 && !hasStdin {
			hd := st.heredocs[0]
			return hd.body, hd.literal, true
		}
		return st.heredocs[0].body, false, true
	}
	if len(st.herestrings) > 0 {
		if len(st.herestrings) == 1 && !hasStdin {
			hs := st.herestrings[0]
			return hs.body, hs.literal, true
		}
		return st.herestrings[0].body, false, true
	}
	if hasStdin {
		return stdinText, true, true
	}
	return "", false, false
}

func stageStdoutRedirect(redirects []bashRedirect, stdoutTo string, appendFlag bool) (path string, literal, appendMode, conflicts bool) {
	count := 0
	for _, r := range redirects {
		switch r.op {
		case ">", ">|", "1>", "&>", ">&":
			if r.op == ">&" && isFileDescriptorTarget(r.target.value) {
				conflicts = true
				continue
			}
			if !isStdoutFileTarget(r.target.value) {
				if r.target.value != "/dev/null" {
					conflicts = true
				}
				continue
			}
			count++
			path = r.target.value
			literal = r.target.literal && (r.op == ">" || r.op == ">|" || r.op == "1>")
			appendMode = false
		case ">>", "1>>", "&>>":
			if !isStdoutFileTarget(r.target.value) {
				if r.target.value != "/dev/null" {
					conflicts = true
				}
				continue
			}
			count++
			path = r.target.value
			literal = r.target.literal && (r.op == ">>" || r.op == "1>>")
			appendMode = true
		}
	}
	if stdoutTo != "" && stdoutTo != "/dev/null" {
		count++
		path = stdoutTo
		literal = true
		appendMode = appendFlag
	}
	if count > 1 {
		conflicts = true
	}
	return path, literal, appendMode, conflicts
}

func stageOutputSink(st bashStage, stdoutTo string, appendFlag bool) (path string, appendMode, exact, ok bool) {
	outPath, outLit, outApp, outConflicts := stageStdoutRedirect(st.redirects, stdoutTo, appendFlag)
	if len(st.argv) > 0 {
		prog := filepath.Base(st.argv[0].value)
		switch prog {
		case "tee":
			if teePath, teeLit, teeApp, teeExact, teeOK := parseTeeArgs(st.argv[1:]); teeOK {
				if outPath != "" {
					return teePath, teeApp, false, true
				}
				return teePath, teeApp, teeLit && teeExact && !outConflicts, true
			}
		case "dd":
			if ddPath, ddLit, ddApp, ddExact, ddOK := parseDDArgs(st.argv[1:]); ddOK {
				if outPath != "" {
					return ddPath, ddApp, false, true
				}
				return ddPath, ddApp, ddLit && ddExact && !outConflicts, true
			}
		}
	}
	if outPath != "" {
		return outPath, outApp, outLit && !outConflicts, true
	}
	return "", false, false, false
}

func consumerSinkStage(st bashStage, stdoutTo string, appendFlag bool) (path string, appendMode, exact, ok bool) {
	if len(st.heredocs) > 0 || len(st.herestrings) > 0 {
		return "", false, false, false
	}
	return stageOutputSink(st, stdoutTo, appendFlag)
}

func hasInputOrStderrRedirect(redirects []bashRedirect) bool {
	for _, r := range redirects {
		switch r.op {
		case "<", "0<", "<&", "0<&", "2>", "2>>", "1>&", "2>&":
			return true
		case ">&":
			if isFileDescriptorTarget(r.target.value) || strings.HasPrefix(r.target.value, "&") {
				return true
			}
		case ">", ">>", "1>", "1>>", ">|", "&>":
			if strings.HasPrefix(r.target.value, "&") {
				return true
			}
		}
	}
	return false
}

func isFileDescriptorTarget(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if target == "-" {
		return true
	}
	for i := 0; i < len(target); i++ {
		if target[i] < '0' || target[i] > '9' {
			return false
		}
	}
	return true
}

func isStdoutFileTarget(target string) bool {
	target = strings.TrimSpace(target)
	return target != "" && target != "-" && target != "/dev/null" && !strings.HasPrefix(target, "&")
}

func isRepresentableWriteTarget(path string) bool {
	if path == "" || path != strings.TrimSpace(path) {
		return false
	}
	return replacementPathsRepresentable(ReplacementCall{Tool: "write", Args: map[string]any{"path": path}})
}

func buildWriteCall(path, content string, appendMode bool) ReplacementCall {
	args := map[string]any{
		"path":    path,
		"content": content,
	}
	if appendMode {
		args["append"] = true
	}
	return ReplacementCall{Tool: "write", Args: args}
}
