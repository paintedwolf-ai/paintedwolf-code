import { Text } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import type { AgentSessionPresence, AgentRead, SecurityFinding } from "../../../api/types.ts";
import { agentDocumentMarks } from "../../../files/components/agent-presence.ts";
import { currentAgentSteps, type PaintedAgentMark } from "../../../files/documents/document-agent-presence.ts";
import { agentMarkLines, lineFacts } from "./line-facts.ts";

const doc = Text.of(["one", "two", "three", "four", "five"]);
const read = (id: string, sequence: number, fields: Partial<AgentRead> = {}): AgentRead => ({
  id, sequence, tool_call_id: `call-${id}`, tool: "read", root_id: "root", path: "a.go",
  extent: "range", ranges: [{ start_line: 2, end_line: 3 }], stale: false, ...fields,
});
function marks(fields: Partial<AgentSessionPresence>): PaintedAgentMark[] {
  const presence: AgentSessionPresence = { session_id: "chat", title: "Config defaults", turn: 3, reads: [], intents: [], activities: [], worker_drafts: [], ...fields };
  return agentDocumentMarks(new Map([[presence.session_id, presence]]), { reads: true, changes: true, fileNames: true }, "chat", "root", "a.go")
    .map(mark => ({ mark, from: mark.range ? doc.line(mark.range.start_line).from : 0,
      to: mark.range ? doc.line(mark.range.end_line).to : 0, color: undefined, bornAt: 1, leftAt: null }));
}
const project = (paint: PaintedAgentMark[], line = 2) => lineFacts(line, { doc, marks: paint, attribution: [], findings: [] });
describe("line facts", () => {
  it("retains all reads while text paints only the current operation", () => {
    const paint = marks({ reads: [read("old", 1), read("whole", 2, { extent: "whole_file", ranges: [] }), read("new", 3)],
      intents: [{ id: "intent", tool_call_id: "edit", checkpoint_id: "approval", tool: "replace_lines", operation: "edit", state: "awaiting_approval", root_id: "root", path: "a.go", extent: "range", ranges: [{ start_line: 2, end_line: 3 }] }] });
    const facts = project(paint);
    expect(facts.now.map(row => row.target.kind === "chat" && row.target.toolCallId)).toEqual(["edit", "call-new", "call-whole", "call-old"]);
    expect(facts.now[0]?.waiting).toBe(true);
    expect(facts.now[0]?.target).toMatchObject({ checkpointId: "approval" });
    expect(currentAgentSteps(paint).map(item => item.mark.source.kind)).toEqual(["intent"]);
    expect(project(paint, 5).now.map(row => row.target.kind === "chat" && row.target.toolCallId)).toEqual(["call-whole"]);
  });
  it("selects by host sequence, keeps a current read's ranges, and lets a newer whole read supersede them", () => {
    const paint = marks({ reads: [read("new", 4, { ranges: [{ start_line: 2, end_line: 2 }, { start_line: 4, end_line: 4 }] }), read("old", 1)] });
    expect(currentAgentSteps(paint).map(item => item.mark.key)).toEqual(["new:0", "new:1"]);
    const newer = marks({ reads: [read("old", 1), read("whole", 5, { extent: "whole_file", ranges: [] })] });
    expect(currentAgentSteps(newer).map(item => item.mark.kind)).toEqual(["whole"]);
  });
  it("deduplicates overlapping ranges from one read without dropping other calls", () => {
    const paint = marks({ reads: [read("a", 1, { ranges: [{ start_line: 1, end_line: 3 }, { start_line: 2, end_line: 4 }] }), read("b", 2)] });
    expect(project(paint).now).toHaveLength(2);
  });
  it("uses mapped positions, excludes an exclusive end, and withdraws departed actions", () => {
    const [paint] = marks({ reads: [read("a", 1)] });
    const moved = { ...paint!, from: doc.line(3).from, to: doc.line(5).from };
    expect(agentMarkLines(doc, moved)).toEqual({ first: 3, last: 4 });
    expect(project([moved], 2).count).toBe(0);
    expect(project([moved], 3).now[0]?.fact).toContain("lines 3–4");
    expect(project([{ ...moved, leftAt: 10 }], 3).count).toBe(0);
  });
  it("carries worker identity and whole-file delete intent without guessing from labels", () => {
    const paint = marks({ reads: [read("a", 1, { worker_id: "reader" })],
      worker_drafts: [{ worker_id: "draft", state: "ready", root_id: "root", path: "a.go", extent: "whole_file", ranges: [], insertions: 4, deletions: 3 }],
      intents: [{ id: "i", tool_call_id: "delete", tool: "delete", operation: "delete", state: "pending", root_id: "root", path: "a.go", extent: "whole_file", ranges: [] }] });
    const facts = project(paint);
    expect(facts.now[0]?.fact).toBe("About to delete the whole file");
    expect(facts.now[1]?.target).toEqual({ kind: "worker", sessionId: "chat", jobId: "draft" });
    expect(facts.now[1]?.fact).toContain("+4 −3");
    expect(facts.now[2]?.target).toEqual({ kind: "chat", sessionId: "chat", jobId: "reader", toolCallId: "call-a" });
  });
  it("orders attribution and findings and keeps inactive contributors neutral", () => {
    const finding = (level: SecurityFinding["level"]): SecurityFinding => ({ rule_id: level, level, message: level,
      locations: [], fingerprints: { primary: level }, tool: { name: "opengrep", driver_id: "opengrep" } });
    const facts = lineFacts(2, { doc, marks: [], sessionTitles: { old: "Earlier chat" },
      attribution: [{ startLine: 2, endLine: 3, contributors: [
        { sessionId: "old", turn: 1, ts: "2026-09-12T00:00:00Z", toolCallId: "a" },
        { sessionId: "new", turn: 2, ts: "2026-09-13T00:00:00Z", toolCallId: "b" },
      ] }], findings: [{ line: 2, level: "high", findings: [finding("low"), finding("high")] }] });
    expect(facts.changed.map(row => row.target.kind === "chat" && row.target.toolCallId)).toEqual(["b", "a"]);
    expect(facts.changed[1]?.title).toBe("Earlier chat");
    expect(facts.changed.every(row => row.color === undefined)).toBe(true);
    expect(facts.findings.map(row => row.title)).toEqual(["high", "low"]);
  });
});
