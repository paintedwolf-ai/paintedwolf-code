import { describe, expect, it } from "vitest";
import type { ActionDescriptor, CommandExecutionReceipt } from "../../api/types.ts";
import { ACTION_OUTPUT_LINES, actionOutput } from "./action-output.ts";

const action = (status: ActionDescriptor["status"]): ActionDescriptor => ({ label: "Run command", status, ran: { text: "x", noun: "command" }, facts: [], row: [], links: [] });

function execution(state: string, outputs: { stream: "stdout" | "stderr"; preview: string; tail?: string; truncated?: boolean; lines?: number }[], status_code?: number): CommandExecutionReceipt {
  return { state, status_code, outputs: outputs.map((o) => ({ stream: o.stream, ref: o.stream, size_bytes: o.preview.length + (o.tail?.length ?? 0), complete: !o.truncated, redacted: false, preview: o.preview, preview_truncated: o.truncated ?? false, tail_preview: o.tail, line_count: o.lines })) } as unknown as CommandExecutionReceipt;
}

describe("actionOutput", () => {
  it("shows a failure's error tail over its standard output", () => {
    const rows = [execution("failed", [{ stream: "stdout", preview: "build\nok" }, { stream: "stderr", preview: "head", tail: "frame\nlast line", truncated: true, lines: 400 }], 1)];
    const shown = actionOutput(action("error"), rows);
    expect(shown?.stream).toBe("stderr");
    expect(shown?.tail).toBe(true);
    expect(shown?.lines.at(-1)).toBe("last line");
  });

  it("shows the start of a success and the end of a running action", () => {
    const text = Array.from({ length: 10 }, (_, i) => `line ${i}`).join("\n");
    const rows = [execution("completed", [{ stream: "stdout", preview: text, lines: 10 }], 0)];
    expect(actionOutput(action("done"), rows)?.lines).toEqual(["line 0", "line 1", "line 2", "line 3"]);
    expect(actionOutput(action("running"), rows)?.lines).toEqual(["line 6", "line 7", "line 8", "line 9"]);
    expect(actionOutput(action("done"), rows)?.lines).toHaveLength(ACTION_OUTPUT_LINES);
  });

  it("falls back to the host's result for a refusal with no executions", () => {
    expect(actionOutput(action("error"), [], "refused: writes go through Write file")?.stream).toBe("result");
    expect(actionOutput(action("done"), [])).toBeUndefined();
  });
});
