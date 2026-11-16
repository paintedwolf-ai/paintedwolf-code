import type { ActionDescriptor, CommandExecutionOutput, CommandExecutionReceipt } from "../../api/types.ts";

export const ACTION_OUTPUT_LINES = 4;

export type ActionOutput = {
  stream: "stdout" | "stderr" | "result";
  lines: string[];
  /** The lines are the end of the output rather than its start. */
  tail: boolean;
  /** Describes output beyond the shown lines, or undefined when all of it is shown. */
  hidden?: string;
};

const FAILED_STATES = new Set(["failed", "refused", "interrupted"]);

function splitLines(text: string): string[] {
  const lines = text.replace(/\n$/, "").split("\n");
  return lines.length === 1 && lines[0] === "" ? [] : lines;
}

function fromExecution(output: CommandExecutionOutput, tail: boolean): ActionOutput | undefined {
  const preview = splitLines(output.preview);
  const continues = output.preview_truncated || !output.complete;
  let lines: string[];
  if (tail && continues && output.tail_preview) lines = splitLines(output.tail_preview);
  else if (tail) lines = preview.slice(-ACTION_OUTPUT_LINES);
  else lines = preview.slice(0, ACTION_OUTPUT_LINES);
  if (lines.length === 0) return undefined;
  const total = output.line_count;
  let hidden: string | undefined;
  if (total !== undefined && total > lines.length) hidden = `${tail ? "Last" : "First"} ${lines.length} of ${total.toLocaleString()} lines`;
  else if (total === undefined && (continues || preview.length > lines.length)) hidden = "Output continues";
  return { stream: output.stream, lines, tail, hidden };
}

/**
 * The output worth showing for an action: the failing execution's error tail
 * after a failure, the live tail while running, otherwise the start of the
 * last execution's output. A refusal with no executions shows the host's result.
 */
export function actionOutput(action: ActionDescriptor, executions: readonly CommandExecutionReceipt[], result?: string | null): ActionOutput | undefined {
  if (action.status === "error") {
    for (let i = executions.length - 1; i >= 0; i--) {
      const row = executions[i]!;
      if (!FAILED_STATES.has(row.state) && !(row.status_code !== undefined && row.status_code !== 0)) continue;
      const stderr = row.outputs?.find((output) => output.stream === "stderr" && output.size_bytes > 0);
      const stdout = row.outputs?.find((output) => output.stream === "stdout" && output.size_bytes > 0);
      const shown = (stderr && fromExecution(stderr, true)) ?? (stdout && fromExecution(stdout, true));
      if (shown) return shown;
    }
    if (result && executions.every((row) => !row.outputs?.length)) {
      const lines = splitLines(result);
      return { stream: "result", lines: lines.slice(-ACTION_OUTPUT_LINES), tail: true, hidden: lines.length > ACTION_OUTPUT_LINES ? "Output continues" : undefined };
    }
    return undefined;
  }
  for (let i = executions.length - 1; i >= 0; i--) {
    const output = executions[i]?.outputs?.find((item) => item.stream === "stdout" && item.size_bytes > 0)
      ?? executions[i]?.outputs?.find((item) => item.stream === "stderr" && item.size_bytes > 0);
    const shown = output && fromExecution(output, action.status === "running");
    if (shown) return shown;
  }
  return undefined;
}
