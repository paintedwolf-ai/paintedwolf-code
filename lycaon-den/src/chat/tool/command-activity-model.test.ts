import { describe, expect, it } from "vitest";
import type { CommandActivity, CommandProgramReceipt } from "../../api/types.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import { commandSourceSegments, createCommandActivityProjection } from "./command-activity-model.ts";

function fact(id: string, overrides: Partial<CommandActivity> = {}): CommandActivity {
  return { id, group: "Exploring project files", label: "Read source", subject: "source.rs", state: "completed", outcome: "Exited 0", status_code: 0, execution_ids: [id], spans: [], ...overrides };
}
function entry(id: string, facts?: CommandActivity[]): ActivitySpanEntry {
  const program = facts ? { id: `program:${id}`, activities: facts, state: "completed", source_ref: "source" } as CommandProgramReceipt : undefined;
  return { kind: "tool", part: { id, toolCallId: id, assistantMessageId: "assistant", messageId: "result", tool: "command", kind: "command", status: "completed", commandProgram: program } };
}
function parts(entries: ActivitySpanEntry[]): ToolPartView[] { return entries.flatMap(entry => entry.kind === "tool" ? [entry.part] : []); }

describe("command activity composition", () => {
 it("splits one call without changing the first row identity", () => {
  const project = createCommandActivityProjection();
  const rows = parts(project([entry("call", [fact("build"), fact("test")])]));
  expect(rows.map(row => row.id)).toEqual(["call", "call:activity:test"]);
 });
 it("appends a new observation directly to its established row", () => {
  const project = createCommandActivityProjection();
  const first = entry("a", [fact("read-a", { merge_key: "scope:path" })]);
  const before = parts(project([first]));
  const after = parts(project([first, entry("b", [fact("read-b", { merge_key: "scope:path" })])]));
  expect(after.map(row => row.id)).toEqual(before.map(row => row.id));
  expect(after[0]!.commandActivity!.members).toHaveLength(2);
 });
 it("never merges visible rows when late metadata supplies matching keys", () => {
  const project = createCommandActivityProjection();
  const before = parts(project([entry("a", [fact("a")]), entry("b", [fact("b")])]));
  const after = parts(project([entry("a", [fact("a", { merge_key: "same" })]), entry("b", [fact("b", { merge_key: "same" })])]));
  expect(after.map(row => row.id)).toEqual(before.map(row => row.id));
 });
 it("keeps labels, targets and positions stable while outcomes change", () => {
  const project = createCommandActivityProjection();
  project([entry("a", [fact("a", { state: "running", outcome: "Running" })])]);
  const [row] = parts(project([entry("a", [fact("a", { label: "Different", subject: "different", state: "failed", outcome: "Failed" })])]));
  expect(row!.commandActivity!.fact.label).toBe("Read source");
  expect(row!.title).toBe("source.rs");
  expect(row!.status).toBe("error");
 });
 it("does not fold across a mutation or other transcript boundary", () => {
  const project = createCommandActivityProjection();
  const rows = project([entry("a", [fact("a", { merge_key: "same" })]), entry("edit", [fact("edit", { label: "Change files" })]), entry("b", [fact("b", { merge_key: "same" })])]);
  expect(rows).toHaveLength(3);
 });
 it("uses live facts before the canonical result arrives", () => {
  const project = createCommandActivityProjection();
  const row = entry("a");
  if (row.kind === "tool") row.part.status = "running";
  expect(project([row])).toHaveLength(0);
  const rows = parts(project([row], () => ({ id: "live", activities: [fact("live")], state: "running" }) as CommandProgramReceipt));
  expect(rows[0]!.id).toBe("a");
 });
});

describe("original command spans", () => {
 const program = { source_ref: "root" } as CommandProgramReceipt;
 it("uses byte offsets with Unicode and preserves the whole command", () => {
  const source = "echo 🐺 && cargo test";
  const start = new TextEncoder().encode("echo 🐺 && ").length;
  const segments = commandSourceSegments(source, program, fact("test", { spans: [{ source_ref: "root", start_byte: start, end_byte: new TextEncoder().encode(source).length }] }));
  expect(segments.map(segment => segment.text).join("")).toBe(source);
  expect(segments.find(segment => segment.highlighted)?.text).toBe("cargo test");
 });
 it("does not apply foreign, malformed or redacted coordinates", () => {
  const value = fact("x", { spans: [{ source_ref: "other", start_byte: 0, end_byte: 4 }] });
  expect(commandSourceSegments("echo", program, value).some(segment => segment.highlighted)).toBe(false);
  expect(commandSourceSegments("echo", program, { ...value, spans: [{ source_ref: "root", start_byte: 0, end_byte: 999 }] }).some(segment => segment.highlighted)).toBe(false);
  expect(commandSourceSegments("echo", program, value, true)).toEqual([{ text: "echo", highlighted: false }]);
 });
});

it("appends late starts after visible rows instead of inserting above them", () => {
 const project = createCommandActivityProjection();
 const a = entry("a", [fact("a")]);
 const b = entry("b", [fact("b")]);
 project([a,b]);
 const rows = parts(project([entry("a", [fact("a"),fact("late")]),b]));
 expect(rows.map(row=>row.id)).toEqual(["a","b","a:activity:late"]);
});
