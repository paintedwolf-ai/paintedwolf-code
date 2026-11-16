import { Text } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import type { AgentSessionPresence, AgentRead, SecurityFinding } from "../../../api/types.ts";
import { agentDocumentMarks } from "../../../files/components/agent-presence.ts";
import { currentAgentSteps, type PaintedAgentMark } from "../../../files/documents/agent-paint-lifetime.ts";
import { agentMarkLines, lineFacts, lineFactsHint, lineFactsHintLabel } from "./line-facts.ts";

const doc = Text.of(["one", "two", "three", "four", "five"]);
const read = (id: string, sequence: number, fields: Partial<AgentRead> = {}): AgentRead => ({
  id, sequence, tool_call_id: `call-${id}`, tool: "read", root_id: "root", path: "a.go",
  extent: "range", ranges: [{ start_line: 2, end_line: 3 }], stale: false, ...fields,
});
function marks(fields: Partial<AgentSessionPresence>): PaintedAgentMark[] {
  const presence: AgentSessionPresence = { session_id: "chat", title: "Config defaults", turn: 3, reads: [], intents: [], activities: [], worker_drafts: [], ...fields };
  return agentDocumentMarks(new Map([[presence.session_id, presence]]), { reads: true, changes: true, fileNames: true }, "chat", "root", "a.go")
    .map(mark => ({ mark, from: mark.range ? doc.line(mark.range.start_line).from : 0,
      to: mark.range ? doc.line(mark.range.end_line).to : 0, color: undefined, bornAt: 1, leftAt: null, supersededAt: null }));
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
    expect(currentAgentSteps(paint, 1).map(item => item.mark.source.kind)).toEqual(["intent"]);
    expect(project(paint, 5).now.map(row => row.target.kind === "chat" && row.target.toolCallId)).toEqual(["call-whole"]);
  });
  it("selects by host sequence, keeps a current read's ranges, and lets a newer whole read supersede them", () => {
    const paint = marks({ reads: [read("new", 4, { ranges: [{ start_line: 2, end_line: 2 }, { start_line: 4, end_line: 4 }] }), read("old", 1)] });
    expect(currentAgentSteps(paint, 1).map(item => item.mark.key)).toEqual(["new:0", "new:1"]);
    const newer = marks({ reads: [read("old", 1), read("whole", 5, { extent: "whole_file", ranges: [] })] });
    expect(currentAgentSteps(newer, 1).map(item => item.mark.kind)).toEqual(["whole"]);
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

  it("derives the hint from the first card row and keeps relative time out of fact text", () => {
    const levels: SecurityFinding["level"][] = ["critical", "high", "medium"];
    const nowPool = marks({
      reads: [read("r1", 1)],
      worker_drafts: [{ worker_id: "w1", state: "ready", root_id: "root", path: "a.go", extent: "range", ranges: [{ start_line: 2, end_line: 3 }], insertions: 1, deletions: 1 }],
      intents: [{ id: "i1", tool_call_id: "edit", checkpoint_id: "cp", tool: "replace_lines", operation: "edit", state: "awaiting_approval", root_id: "root", path: "a.go", extent: "range", ranges: [{ start_line: 2, end_line: 3 }] }],
    });
    const baseMs = Date.now() - 15 * 60_000;
    for (let nowCount = 0; nowCount <= 3; nowCount++) {
      for (let changedCount = 0; changedCount <= 3; changedCount++) {
        for (let findingCount = 0; findingCount <= 3; findingCount++) {
          const contributors = Array.from({ length: changedCount }, (_, i) => ({
            sessionId: `s-${i}`,
            turn: i + 1,
            ts: new Date(baseMs + i * 60_000).toISOString(),
            toolCallId: `tc-${i}`,
          }));
          const findingItems: SecurityFinding[] = levels.slice(0, findingCount).map(level => ({
            rule_id: level,
            level,
            message: `msg-${level}`,
            locations: [],
            fingerprints: { primary: `fp-${level}` },
            tool: { name: "opengrep", driver_id: "opengrep" },
          }));
          const facts = lineFacts(2, {
            doc,
            marks: nowPool.slice(0, nowCount),
            attribution: changedCount ? [{ startLine: 2, endLine: 3, contributors }] : [],
            findings: findingCount ? [{ line: 2, level: findingItems[0]!.level, findings: findingItems }] : [],
          });
          const timeless = lineFacts(2, {
            doc,
            marks: nowPool.slice(0, nowCount),
            attribution: changedCount ? [{ startLine: 2, endLine: 3, contributors: contributors.map(c => ({ ...c, ts: "" })) }] : [],
            findings: findingCount ? [{ line: 2, level: findingItems[0]!.level, findings: findingItems }] : [],
          });
          expect(facts.count).toBe(nowCount + changedCount + findingCount);
          const hint = lineFactsHint(facts);
          if (facts.count === 0) {
            expect(hint).toBeNull();
            continue;
          }
          const rows = [...facts.now, ...facts.changed, ...facts.findings];
          expect(hint).not.toBeNull();
          expect(hint!.lead).toBe(rows[0]);
          expect(hint!.more).toBe(facts.count - 1);
          const baseLabel = hint!.lead.target.kind === "finding"
            ? `${hint!.lead.fact} · ${hint!.lead.title}`
            : hint!.lead.fact;
          expect(lineFactsHintLabel(hint!)).toBe(
            hint!.more > 0 ? `${baseLabel} · ${hint!.more} more` : baseLabel,
          );
          for (let i = 0; i < facts.changed.length; i++) {
            const row = facts.changed[i]!;
            expect(row.when).toBeTruthy();
            expect(row.fact).toBe(timeless.changed[i]!.fact);
            expect(row.fact.includes(row.when!)).toBe(false);
          }
          for (const row of rows) {
            if (row.when) expect(row.fact.includes(row.when)).toBe(false);
          }
        }
      }
    }
  });
});
